package posthog

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const fixtureToken = "phc_fixture0capture0token0for0tests0only0000000"

// fixture returns a recorded body and its request's metadata.
func fixture(t testing.TB, name string) ([]byte, recorded) {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", name+".body"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	var meta recorded
	data, err := os.ReadFile(filepath.Join("testdata", name+".request.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &meta); err != nil {
		t.Fatal(err)
	}
	return body, meta
}

type recorded struct {
	Method  string              `json:"method"`
	Path    string              `json:"path"`
	Query   string              `json:"query"`
	Headers map[string][]string `json:"headers"`
}

func (r recorded) header(name string) string {
	if v := r.Headers[name]; len(v) > 0 {
		return v[0]
	}
	return ""
}

func unzipped(t testing.TB, b []byte) []byte {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if _, err := out.ReadFrom(zr); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func gzipped(t testing.TB, b []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	zw.Write(b)
	zw.Close()
	return buf.Bytes()
}

func TestPosthog_GzipJS(t *testing.T) {
	// posthog-js 1.438 gzips the body and says so only by its first bytes
	// (text/plain, no query): the history page view as it was recorded.
	body, meta := fixture(t, "04-pageview-history")
	batch, err := ParseBody(body, Options{ContentType: meta.header("Content-Type")})
	if err != nil {
		t.Fatal(err)
	}
	if batch.Token != fixtureToken || len(batch.Events) != 1 {
		t.Fatalf("batch = token %q, %d events", batch.Token, len(batch.Events))
	}
	ev := batch.Events[0]
	if ev.Name != "$pageview" || ev.UUID == "" || ev.PageviewID != ev.UUID || ev.PrevPageviewID == "" ||
		ev.SessionID == "" || ev.WindowID == "" || ev.DistinctID == "" || ev.Token != fixtureToken {
		t.Errorf("event = %+v", ev)
	}
	if ev.URL != "http://localhost:5173/checkout?step=2" || ev.Timestamp.IsZero() || batch.SentAt.IsZero() {
		t.Errorf("url %q, timestamp %v, sent_at %v", ev.URL, ev.Timestamp, batch.SentAt)
	}

	// Older posthog-js says compression=gzip-js in the query; the same body.
	again, err := ParseBody(body, Options{ContentType: "text/plain", Compression: "gzip-js"})
	if err != nil || len(again.Events) != 1 || again.Events[0].UUID != ev.UUID {
		t.Errorf("with compression=gzip-js: %+v, %v", again, err)
	}
}

func TestPosthog_Base64Form(t *testing.T) {
	plain := unzipped(t, mustFixture(t, "02-pageview-load"))
	encoded := base64.StdEncoding.EncodeToString(plain)

	// posthog-js with compression base64: a form with data=<base64>.
	form := "data=" + url.QueryEscape(encoded)
	batch, err := ParseBody([]byte(form), Options{ContentType: "application/x-www-form-urlencoded", Compression: "base64"})
	if err != nil || len(batch.Events) != 1 || batch.Events[0].Name != "$pageview" {
		t.Fatalf("base64 form: %+v, %v", batch, err)
	}
	// The compression can be a form field too, and "+" may arrive as a space.
	form = "compression=base64&data=" + strings.ReplaceAll(encoded, "+", " ")
	if batch, err := ParseBody([]byte(form), Options{ContentType: "application/x-www-form-urlencoded"}); err != nil || len(batch.Events) != 1 {
		t.Errorf("base64 form field: %+v, %v", batch, err)
	}
	// A GET with ?data= (the pixel fallback), base64 without saying so.
	if batch, err := ParseBody(nil, Options{Data: encoded}); err != nil || len(batch.Events) != 1 {
		t.Errorf("GET data: %+v, %v", batch, err)
	}
	// Plain JSON in the form.
	if batch, err := ParseBody([]byte("data="+url.QueryEscape(string(plain))), Options{ContentType: "application/x-www-form-urlencoded"}); err != nil || len(batch.Events) != 1 {
		t.Errorf("JSON form: %+v, %v", batch, err)
	}
}

func mustFixture(t testing.TB, name string) []byte {
	body, _ := fixture(t, name)
	return body
}

func TestPosthog_Shapes(t *testing.T) {
	sent := `"sent_at":"2026-10-16T12:00:05Z"`
	cases := map[string]string{
		// /batch/ from server SDKs: a top-level key and events with distinct_id at their top.
		"batch":  `{"api_key":"phc_a",` + sent + `,"batch":[{"event":"$pageview","distinct_id":"42","properties":{"$current_url":"https://shop.example.com/"}},{"event":"signup","distinct_id":7}]}`,
		"array":  `[{"event":"$pageview","properties":{"token":"phc_a","distinct_id":"42"}},{"event":"signup","properties":{"token":"phc_a"}}]`,
		"single": `{"event":"$pageview","api_key":"phc_a","distinct_id":"42","properties":{}}` + "\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			batch, err := ParseBody([]byte(body), Options{ContentType: "application/json"})
			if err != nil {
				t.Fatal(err)
			}
			if n := len(batch.Events); n < 1 {
				t.Fatalf("%d events", n)
			}
			ev := batch.Events[0]
			if ev.Name != "$pageview" || ev.DistinctID != "42" || (batch.Token != "phc_a" && ev.Token != "phc_a") {
				t.Errorf("event %+v, batch token %q", ev, batch.Token)
			}
		})
	}
	// A distinct_id sent as a number keeps its digits.
	batch, _ := ParseBody([]byte(cases["batch"]), Options{})
	if got := batch.Events[1].DistinctID; got != "7" {
		t.Errorf("numeric distinct_id = %q", got)
	}
	// Content-Encoding gzip is undone by the handler; gzip without it is found by its first bytes.
	if batch, err := ParseBody(gzipped(t, []byte(cases["batch"])), Options{ContentType: "application/json"}); err != nil || len(batch.Events) != 2 {
		t.Errorf("gzip JSON: %+v, %v", batch, err)
	}
}

func TestPosthog_BadBodies(t *testing.T) {
	cases := map[string]struct {
		body string
		opts Options
	}{
		"empty":             {"", Options{}},
		"not JSON":          {"hello", Options{ContentType: "text/plain"}},
		"truncated":         {`{"batch":[{"event":"$pageview"`, Options{}},
		"no events":         {`{"api_key":"phc_a"}`, Options{}},
		"item not object":   {`{"batch":[1,2]}`, Options{}},
		"lz64":              {"data=abc", Options{ContentType: "application/x-www-form-urlencoded", Compression: "lz64"}},
		"bad gzip":          {"\x1f\x8b\x08\x00garbage", Options{}},
		"bad base64":        {"data=%%%", Options{ContentType: "application/x-www-form-urlencoded", Compression: "base64"}},
		"form without data": {"x=1", Options{ContentType: "application/x-www-form-urlencoded"}},
	}
	for name, c := range cases {
		if _, err := ParseBody([]byte(c.body), c.opts); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestPosthog_Limits(t *testing.T) {
	// Decompressed bodies are limited too (a zip bomb stops at the limit).
	big := gzipped(t, bytes.Repeat([]byte(" "), 2<<20))
	if _, err := ParseBody(big, Options{MaxBytes: 1 << 20}); !errors.Is(err, ErrTooLarge) {
		t.Errorf("2 MiB of spaces under a 1 MiB limit: err = %v, want ErrTooLarge", err)
	}
	// One large event is dropped; the others stay.
	large := `{"event":"custom","properties":{"blob":"` + strings.Repeat("x", 2000) + `"}}`
	body := `{"api_key":"phc_a","batch":[{"event":"$pageview"},` + large + `,{"properties":{}}]}`
	batch, err := ParseBody([]byte(body), Options{MaxEventBytes: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Events) != 1 || batch.Dropped != 2 {
		t.Errorf("events %d, dropped %d; want 1 kept, the large one and the nameless one dropped", len(batch.Events), batch.Dropped)
	}
}

func TestEvent_Time(t *testing.T) {
	received := time.Date(2026, 10, 16, 12, 0, 10, 0, time.UTC)
	ts := time.Date(2026, 10, 16, 13, 0, 0, 0, time.UTC) // the client's clock is an hour fast
	sent := ts.Add(5 * time.Second)
	cases := []struct {
		name string
		ev   Event
		want time.Time
	}{
		{"clock corrected by sent_at", Event{Timestamp: ts, SentAt: sent}, received.Add(-5 * time.Second)},
		{"offset", Event{Timestamp: ts, Offset: 3 * time.Second, HasOffset: true}, received.Add(-3 * time.Second)},
		{"timestamp only", Event{Timestamp: ts}, ts},
		{"nothing", Event{}, received},
	}
	for _, c := range cases {
		if got := c.ev.Time(received); !got.Equal(c.want) {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

func TestPosthog_ScrubsProperties(t *testing.T) {
	body := `{"api_key":"phc_a","batch":[{"event":"custom","properties":{
		"$current_url":"https://shop.example.com/reset?token=tok_abc123&step=2",
		"$session_id":"0199-session","$pageview_id":"0199-view","distinct_id":"42",
		"$ip":"203.0.113.9","ip":"203.0.113.10","$set":{"email":"jan@example.com","$ip":"203.0.113.11"},
		"password":"hunter2","card_number":"4111111111111111","plan":"pro",
		"headers":{"X-Forwarded-For":"203.0.113.12","Cookie":"laravel_session=abc"},
		"$geo":"{{auto}}"}}]}`
	batch, err := ParseBody([]byte(body), Options{})
	if err != nil {
		t.Fatal(err)
	}
	ev := batch.Events[0]
	ev.Scrub()
	stored, err := json.Marshal(ev.Properties)
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"203.0.113", "tok_abc123", "hunter2", "4111111111111111", "laravel_session", "{{auto}}"} {
		if bytes.Contains(stored, []byte(leak)) {
			t.Errorf("scrubbed properties still contain %q: %s", leak, stored)
		}
	}
	for _, kept := range []string{`"plan":"pro"`, `"distinct_id":"42"`, `"email":"jan@example.com"`, `step=2`} {
		if !bytes.Contains(stored, []byte(kept)) {
			t.Errorf("scrubbed properties lost %s: %s", kept, stored)
		}
	}
	// What the page views need was read before scrubbing.
	if ev.SessionID != "0199-session" || ev.PageviewID != "0199-view" || !strings.Contains(ev.URL, "/reset") {
		t.Errorf("ids and URL = %q %q %q", ev.SessionID, ev.PageviewID, ev.URL)
	}
}

// Postgres refuses a NUL in any text column, and one refused value fails
// the write of the whole batch: every string of an event loses its NULs
// as it is read, the properties (stored as JSON for custom events) too.
func TestPosthog_RemovesNULs(t *testing.T) {
	body := `{"api_key":"phc_a","batch":[{"event":"signed_up\u0000","uuid":"c2\u0000","properties":{
		"distinct_id":"anna\u0000","$current_url":"https://portal.example.com/a\u0000b","$session_id":"s\u0000",
		"$window_id":"w\u0000","$pageview_id":"v\u0000","$raw_user_agent":"UA\u0000","pl\u0000an":"pro\u0000",
		"nested":{"list":["a\u0000"]}}}]}`
	batch, err := ParseBody([]byte(body), Options{})
	if err != nil {
		t.Fatal(err)
	}
	ev := batch.Events[0]
	for name, v := range map[string]string{"name": ev.Name, "uuid": ev.UUID, "distinct_id": ev.DistinctID, "url": ev.URL,
		"session": ev.SessionID, "window": ev.WindowID, "pageview": ev.PageviewID, "user agent": ev.RawUserAgent} {
		if strings.ContainsRune(v, 0) {
			t.Errorf("%s keeps a NUL: %q", name, v)
		}
	}
	if ev.Name != "signed_up" || ev.DistinctID != "anna" || ev.URL != "https://portal.example.com/ab" {
		t.Errorf("name %q, user %q, url %q", ev.Name, ev.DistinctID, ev.URL)
	}
	stored, err := json.Marshal(ev.OwnProperties())
	if err != nil {
		t.Fatal(err)
	}
	if string(stored) != `{"nested":{"list":["a"]},"plan":"pro"}` {
		t.Errorf("own properties = %s", stored)
	}
}

func FuzzParsePosthogBody(f *testing.F) {
	for _, name := range []string{"02-pageview-load", "04-pageview-history", "05-identify", "07-custom", "08-pageleave"} {
		body := mustFixture(f, name)
		f.Add(body, "text/plain", "")
		f.Add(unzipped(f, body), "application/json", "")
		f.Add([]byte("data="+url.QueryEscape(base64.StdEncoding.EncodeToString(unzipped(f, body)))), "application/x-www-form-urlencoded", "base64")
	}
	f.Add([]byte(`[{"event":"x","properties":{"token":"t","distinct_id":1e400}}]`), "", "")
	f.Add([]byte(`{"event":"$pageview","timestamp":12,"offset":"x","sent_at":[]}`), "", "gzip-js")
	f.Fuzz(func(t *testing.T, body []byte, contentType, compression string) {
		batch, err := ParseBody(body, Options{ContentType: contentType, Compression: compression, MaxBytes: 1 << 20, MaxEventBytes: 64 << 10})
		if err != nil {
			return
		}
		for i := range batch.Events {
			ev := &batch.Events[i]
			if ev.Name == "" {
				t.Fatal("an event without a name was kept")
			}
			for _, v := range []string{ev.Name, ev.UUID, ev.DistinctID, ev.URL, ev.SessionID, ev.WindowID, ev.PageviewID} {
				if strings.ContainsRune(v, 0) || len(v) > maxURLBytes {
					t.Fatalf("a value for a column keeps a NUL or is too long: %q", v)
				}
			}
			ev.Scrub()
			ev.Time(time.Now())
			if _, err := json.Marshal(ev.Properties); err != nil {
				t.Fatalf("scrubbed properties do not encode: %v", err)
			}
		}
	})
}
