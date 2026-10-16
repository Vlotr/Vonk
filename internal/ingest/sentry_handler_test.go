package ingest

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"monitor/internal/sentry"
	"monitor/internal/store"
)

func TestEnvelopeHandler_AcceptedEventIsScrubbedAndQueued(t *testing.T) {
	env := newTestEnv(t, 100)
	p := env.project(t, "Webshop", 0)

	rec := env.post(envelopePath(p), envelope(eventIDN(1), ""), authHeader(p.SentryKey))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body %s", rec.Code, rec.Body)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body["id"] != eventIDN(1) {
		t.Errorf(`body = %s, want {"id":"%s"}`, rec.Body, eventIDN(1))
	}

	items := env.drain()
	if len(items) != 1 {
		t.Fatalf("queued %d items, want 1", len(items))
	}
	item := items[0]
	if !item.ReceivedAt.Equal(env.clock.Now()) {
		t.Errorf("ReceivedAt = %v, want %v", item.ReceivedAt, env.clock.Now())
	}
	data, _ := item.Event.JSON()
	if strings.Contains(string(data), "203.0.113.9") || strings.Contains(string(data), "token=abc") {
		t.Errorf("queued event is not scrubbed: %s", data)
	}
}

func TestEnvelopeHandler_Fixtures(t *testing.T) {
	env := newTestEnv(t, 100)
	p := env.project(t, "Webshop", 0)

	for _, name := range []string{"php-exception", "browser-typeerror"} {
		t.Run(name, func(t *testing.T) {
			body, req := fixtureRequest(t, name, p)
			rec := serveRequest(env, req, body)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d; body %s", rec.Code, rec.Body)
			}
			if rec.Header().Get("Access-Control-Allow-Origin") != "*" {
				t.Error("no CORS header on the response")
			}
			items := env.drain()
			if len(items) != 1 || items[0].Event.EventID == "" || len(items[0].Event.Exceptions) != 1 {
				t.Fatalf("queued %d items: %+v", len(items), items)
			}
			var resp map[string]string
			json.Unmarshal(rec.Body.Bytes(), &resp)
			if resp["id"] != items[0].Event.EventID {
				t.Errorf("response id %q, want %q", resp["id"], items[0].Event.EventID)
			}
		})
	}
}

// fixtureRequest rebuilds a recorded SDK request for project p: same method,
// headers, query and body, with p's key instead of the recorded one.
func fixtureRequest(t *testing.T, name string, p store.Project) ([]byte, *http.Request) {
	t.Helper()
	dir := filepath.Join("..", "sentry", "testdata")
	metaJSON, err := os.ReadFile(filepath.Join(dir, name+".headers.json"))
	if err != nil {
		t.Fatal(err)
	}
	var meta struct {
		Method  string      `json:"method"`
		Query   string      `json:"query"`
		Headers http.Header `json:"headers"`
	}
	if err := json.Unmarshal(metaJSON, &meta); err != nil {
		t.Fatal(err)
	}
	bodyName := name + ".envelope"
	if meta.Headers.Get("Content-Encoding") == "gzip" {
		bodyName += ".gz"
	}
	body, err := os.ReadFile(filepath.Join(dir, bodyName))
	if err != nil {
		t.Fatal(err)
	}

	const recordedKey = "5f2b6a0c7d3e4f1a8b9c0d1e2f3a4b5c"
	query := strings.ReplaceAll(meta.Query, recordedKey, p.SentryKey)
	req := httptest.NewRequest(meta.Method, envelopePath(p)+"?"+query, nil)
	for k, values := range meta.Headers {
		for _, v := range values {
			req.Header.Add(k, strings.ReplaceAll(v, recordedKey, p.SentryKey))
		}
	}
	return body, req
}

// serveRequest sends req with body through the handler, from a public address.
func serveRequest(env *testEnv, req *http.Request, body []byte) *httptest.ResponseRecorder {
	req.Body = io.NopCloser(bytes.NewReader(body))
	req.ContentLength = int64(len(body))
	req.RemoteAddr = "198.51.100.7:40000"
	rec := httptest.NewRecorder()
	env.mux.ServeHTTP(rec, req)
	return rec
}

func gzipBytes(t *testing.T, data []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	zw.Write(data)
	zw.Close()
	return buf.Bytes()
}

func TestEnvelopeHandler_Gzip(t *testing.T) {
	env := newTestEnv(t, 100)
	p := env.project(t, "Webshop", 0)
	plain := envelope(eventIDN(1), "")

	var zlibBuf, flateBuf bytes.Buffer
	zw := zlib.NewWriter(&zlibBuf)
	zw.Write(plain)
	zw.Close()
	fw, _ := flate.NewWriter(&flateBuf, flate.DefaultCompression)
	fw.Write(plain)
	fw.Close()

	cases := []struct {
		name, encoding string
		body           []byte
		want           int
	}{
		{"gzip", "gzip", gzipBytes(t, plain), http.StatusOK},
		{"x-gzip, upper case", "X-GZIP", gzipBytes(t, plain), http.StatusOK},
		{"deflate (zlib)", "deflate", zlibBuf.Bytes(), http.StatusOK},
		{"deflate (raw)", "deflate", flateBuf.Bytes(), http.StatusOK},
		{"identity", "identity", plain, http.StatusOK},
		{"unknown encoding", "br", plain, http.StatusBadRequest},
		{"two encodings", "gzip, gzip", gzipBytes(t, gzipBytes(t, plain)), http.StatusBadRequest},
		{"not really gzip", "gzip", plain, http.StatusBadRequest},
		{"truncated gzip", "gzip", gzipBytes(t, plain)[:40], http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := env.post(envelopePath(p), tc.body, authHeader(p.SentryKey), func(r *http.Request) {
				r.Header.Set("Content-Encoding", tc.encoding)
			})
			if rec.Code != tc.want {
				t.Errorf("status = %d, want %d; body %s", rec.Code, tc.want, rec.Body)
			}
			if tc.want == http.StatusBadRequest && rec.Header().Get("X-Sentry-Error") == "" {
				t.Error("400 without X-Sentry-Error")
			}
		})
	}
}

// zeros is an io.Reader of n zero bytes.
type zeros struct{ n int64 }

func (z *zeros) Read(p []byte) (int, error) {
	if z.n <= 0 {
		return 0, io.EOF
	}
	if int64(len(p)) > z.n {
		p = p[:z.n]
	}
	clear(p)
	z.n -= int64(len(p))
	return len(p), nil
}

func TestEnvelopeHandler_BodyLimit(t *testing.T) {
	env := newTestEnv(t, 100)
	p := env.project(t, "Webshop", 0)

	// An envelope just over 20 MiB: a valid event plus a large attachment.
	attachment := bytes.Repeat([]byte("a"), MaxBodyBytes)
	big := append(envelope(eventIDN(1), ""), []byte(`{"type":"attachment"}`+"\n")...)
	big = append(big, attachment...)

	// A zip bomb: 64 MiB of zeros in about 64 KiB of gzip.
	var bomb bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&bomb, gzip.BestSpeed)
	io.Copy(zw, &zeros{n: 64 << 20})
	zw.Close()
	if bomb.Len() > 1<<20 {
		t.Fatalf("bomb is %d bytes compressed; it should be tiny", bomb.Len())
	}

	// Random bytes do not compress, so this gzip body is over 20 MiB as sent.
	noise := make([]byte, MaxBodyBytes+1024)
	rand.NewChaCha8([32]byte{3, 4}).Read(noise)
	noisy := append(envelope(eventIDN(1), ""), []byte(fmt.Sprintf(`{"type":"attachment","length":%d}`+"\n", len(noise)))...)
	noisy = append(noisy, noise...)

	cases := []struct {
		name, encoding string
		body           []byte
	}{
		{"plain body over 20 MiB", "", big},
		{"gzip body over 20 MiB as sent", "gzip", gzipBytes(t, noisy)},
		{"gzip body that inflates past 20 MiB", "gzip", gzipBytes(t, big)},
		{"zip bomb", "gzip", bomb.Bytes()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			start := time.Now()
			rec := env.post(envelopePath(p), tc.body, authHeader(p.SentryKey), func(r *http.Request) {
				if tc.encoding != "" {
					r.Header.Set("Content-Encoding", tc.encoding)
				}
			})
			if rec.Code != http.StatusRequestEntityTooLarge {
				t.Errorf("status = %d, want 413; body %s", rec.Code, rec.Body)
			}
			if d := time.Since(start); d > 5*time.Second {
				t.Errorf("took %v; the limit should stop reading early", d)
			}
		})
	}
	if items := env.drain(); len(items) != 0 {
		t.Errorf("queued %d items from refused bodies", len(items))
	}

	// Just under the limit is fine.
	small := append(envelope(eventIDN(2), ""), []byte(`{"type":"attachment"}`+"\n")...)
	small = append(small, bytes.Repeat([]byte("a"), MaxBodyBytes-len(small)-1)...)
	if rec := env.post(envelopePath(p), small, authHeader(p.SentryKey)); rec.Code != http.StatusOK {
		t.Errorf("body of 20 MiB minus a byte: status = %d, want 200", rec.Code)
	}
}

func TestEnvelopeHandler_AcceptsUnknownItems(t *testing.T) {
	env := newTestEnv(t, 100)
	p := env.project(t, "Webshop", 0)

	withEvent := append([]byte(`{"event_id":"`+eventIDN(1)+`"}`+"\n"+
		`{"type":"thing_from_the_future","length":5,"new":true}`+"\n"+"hello\n"+
		`{"type":"replay_recording"}`+"\n"+`{"segment_id":0}`+"\n"), envelope(eventIDN(1), "")[len(`{"event_id":"`+eventIDN(1)+`"}`+"\n"):]...)
	onlyUnknown := []byte(`{}` + "\n" + `{"type":"thing_from_the_future"}` + "\n" + `{"a":1}` + "\n")

	if rec := env.post(envelopePath(p), withEvent, authHeader(p.SentryKey)); rec.Code != http.StatusOK {
		t.Errorf("unknown items with an event: status = %d; body %s", rec.Code, rec.Body)
	}
	if items := env.drain(); len(items) != 1 {
		t.Errorf("queued %d items, want the 1 event", len(items))
	}
	if rec := env.post(envelopePath(p), onlyUnknown, authHeader(p.SentryKey)); rec.Code != http.StatusOK {
		t.Errorf("only unknown items: status = %d; body %s", rec.Code, rec.Body)
	}
	if items := env.drain(); len(items) != 0 {
		t.Errorf("queued %d items, want none", len(items))
	}
}

func TestEnvelopeHandler_NeverServerError(t *testing.T) {
	env := newTestEnv(t, 100)
	p := env.project(t, "Webshop", 0)
	good := envelope(eventIDN(1), "")

	junk := map[string][]byte{
		"empty":                 nil,
		"newlines":              []byte("\n\n\n"),
		"null bytes":            make([]byte, 1000),
		"JSON array":            []byte(`[1,2,3]`),
		"header only, garbage":  []byte("{}\n\x00\x01\x02"),
		"truncated envelope":    good[:len(good)/2],
		"event not an object":   []byte("{}\n{\"type\":\"event\"}\n\"hello\"\n"),
		"event with wild types": []byte("{}\n{\"type\":\"event\"}\n" + `{"exception":5,"request":{"headers":7,"data":[[null]]},"user":"x","breadcrumbs":{"values":"x"},"timestamp":{},"tags":[[1,2,3]],"fingerprint":"x","level":[]}` + "\n"),
		"deeply nested":         []byte("{}\n{\"type\":\"event\"}\n{\"extra\":" + strings.Repeat("[", 5000) + strings.Repeat("]", 5000) + "}\n"),
		"huge length":           []byte("{}\n{\"type\":\"event\",\"length\":99999999999}\n{}"),
		"length not a number":   []byte("{}\n{\"type\":\"event\",\"length\":\"5\"}\n{}"),
		"invalid utf-8":         append([]byte("{}\n{\"type\":\"event\"}\n{\"message\":\""), 0xff, 0xfe, '"', '}', '\n'),
	}
	rng := rand.New(rand.NewPCG(1, 2))
	for i := range 200 {
		b := make([]byte, rng.IntN(300))
		for j := range b {
			b[j] = byte(rng.IntN(256))
		}
		if i%2 == 0 {
			b = append([]byte("{}\n"), b...)
		}
		junk["random "+string(rune('a'+i%26))+string(rune('a'+i/26))] = b
	}

	for name, body := range junk {
		for _, encoding := range []string{"", "gzip"} {
			payload := body
			if encoding == "gzip" {
				payload = gzipBytes(t, body)
			}
			rec := env.post(envelopePath(p), payload, authHeader(p.SentryKey), func(r *http.Request) {
				if encoding != "" {
					r.Header.Set("Content-Encoding", encoding)
				}
			})
			if rec.Code >= 500 {
				t.Errorf("%s (encoding %q): status %d; body %s", name, encoding, rec.Code, rec.Body)
			}
		}
	}
	env.drain()
}

// Ruling 41: an error's time is moved to the server's clock as PostHog's
// times are: with the envelope's sent_at, the event's timestamp plus
// (received − sent_at), so a client clock that is minutes off cancels
// out; without sent_at (or with one that cannot be read) the timestamp is
// kept as sent, within the clamp.
func TestEnvelopeHandler_CorrectsClockBySentAt(t *testing.T) {
	env := newTestEnv(t, 100)
	p := env.project(t, "Webshop", 1000)
	// Received at 12:00:10 by the server; the error happened a second before.
	happened := time.Date(2026, 10, 16, 12, 0, 9, 0, time.UTC)
	cases := []struct {
		name      string
		timestamp time.Time // by the client's clock
		header    string    // extra envelope header fields
		want      time.Time
	}{
		{"clock 5 minutes ahead", happened.Add(5 * time.Minute), `,"sent_at":"2026-10-16T12:05:10Z"`, happened},
		{"clock 5 minutes behind", happened.Add(-5 * time.Minute), `,"sent_at":"2026-10-16T11:55:10.000Z"`, happened},
		{"clock 2 hours ahead", happened.Add(2 * time.Hour), `,"sent_at":"2026-10-16T14:00:10Z"`, happened},
		{"no sent_at", happened.Add(-3 * time.Minute), ``, happened.Add(-3 * time.Minute)},
		{"sent_at unreadable", happened.Add(-3 * time.Minute), `,"sent_at":"yesterday"`, happened.Add(-3 * time.Minute)},
	}
	for i, c := range cases {
		body := strings.Replace(string(envelope(eventIDN(i+1), c.header)), `"timestamp":1792152010`,
			fmt.Sprintf(`"timestamp":%d`, c.timestamp.Unix()), 1)
		if rec := env.post(envelopePath(p), []byte(body), authHeader(p.SentryKey)); rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", c.name, rec.Code, rec.Body)
		}
	}
	NewWriter(env.db, env.queue, &recordingHooks{}, env.logger).Close()
	for i, c := range cases {
		ev, err := env.db.ErrorEventByEventID(context.Background(), p.ID, eventIDN(i+1))
		if err != nil {
			t.Fatal(err)
		}
		if !ev.Timestamp.Equal(c.want) {
			t.Errorf("%s: stored %v, want %v", c.name, ev.Timestamp.Format(time.TimeOnly), c.want.Format(time.TimeOnly))
		}
	}
}

// Source maps are applied before the event is scrubbed: symbolication
// looks the minified file up by the URL the SDK sent, and the frame URLs
// are masked afterwards (an inline script's page keeps no token).
func TestSentry_SymbolicatesBeforeScrubbing(t *testing.T) {
	env := newTestEnv(t, 100)
	p := env.project(t, "Webshop", 1000)
	token := "4f8c2a9e7b1d3f5a6c8e0b2d4f6a8c0e2b4d6f8a0c2e4b6d8f0a2c4e6b8d0f2a"
	page := "https://shop.example.com/reset-password/" + token

	var seen []string
	env.sentry.Symbolicate = func(_ context.Context, projectID int64, ev *sentry.Event) bool {
		if projectID != p.ID {
			t.Errorf("symbolicated for project %d", projectID)
		}
		seen = append(seen, sentry.MainException(ev).Frames[0].Filename)
		ev.Raw["symbolicated"] = true
		return true
	}
	event := `{"event_id":"` + eventIDN(1) + `","platform":"javascript","exception":{"values":[{"type":"TypeError",` +
		`"stacktrace":{"frames":[{"filename":"` + page + `","function":"onSubmit","lineno":3,"colno":9}]}}]}}`
	body := []byte(`{"event_id":"` + eventIDN(1) + `"}` + "\n" + `{"type":"event"}` + "\n" + event + "\n")
	if rec := env.post(envelopePath(p)+"?sentry_key="+p.SentryKey, body); rec.Code != http.StatusOK {
		t.Fatalf("status = %d %s", rec.Code, rec.Body)
	}
	if len(seen) != 1 || seen[0] != page {
		t.Fatalf("symbolication saw %q, want the URL as sent", seen)
	}
	items := env.drain()
	if len(items) != 1 || items[0].Event.Raw["symbolicated"] != true {
		t.Fatalf("queued = %+v", items)
	}
	if got := sentry.MainException(items[0].Event).Frames[0].Filename; got != "https://shop.example.com/reset-password/:hash" {
		t.Errorf("stored frame file = %q, want it masked", got)
	}
}
