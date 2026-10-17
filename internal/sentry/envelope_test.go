package sentry_test

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"monitor/internal/scrub"
	"monitor/internal/sentry"
)

const maxItem = 1 << 20 // 1 MiB, the limit the ingest handler uses

// fixture returns the bytes of a file in testdata/, gunzipped when it ends in .gz.
func fixture(t testing.TB, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasSuffix(name, ".gz") {
		zr, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		data, err = io.ReadAll(zr)
		if err != nil {
			t.Fatal(err)
		}
	}
	return data
}

func parse(t *testing.T, body []byte) *sentry.Envelope {
	t.Helper()
	env, err := sentry.ParseEnvelope(bytes.NewReader(body), maxItem)
	if err != nil {
		t.Fatalf("ParseEnvelope: %v", err)
	}
	return env
}

// item builds one envelope item. With withLength the header carries the payload length.
func item(header string, payload string, withLength bool) string {
	if withLength {
		header = strings.TrimSuffix(header, "}") + fmt.Sprintf(`,"length":%d}`, len(payload))
	}
	return header + "\n" + payload + "\n"
}

const minimalEvent = `{"event_id":"9ec79c33ec9942ab8353589fcb2e04dc","platform":"php","exception":{"values":[{"type":"LogicException","value":"nope"}]}}`

func TestParseEnvelope_PHPFixture(t *testing.T) {
	env := parse(t, fixture(t, "php-exception.envelope.gz"))

	if len(env.Events) != 1 {
		t.Fatalf("got %d events, want 1", len(env.Events))
	}
	ev := env.Events[0]

	// sentry-php 4.x sends the event id only in the envelope header.
	if ev.EventID != "62173558e0e845b1ab4fd4c6633f22a8" {
		t.Errorf("EventID = %q, want the id from the envelope header", ev.EventID)
	}
	if env.Header.DSN == "" || !strings.Contains(env.Header.DSN, "5f2b6a0c7d3e4f1a8b9c0d1e2f3a4b5c") {
		t.Errorf("Header.DSN = %q", env.Header.DSN)
	}
	if len(ev.Exceptions) != 1 {
		t.Fatalf("got %d exceptions, want 1", len(ev.Exceptions))
	}
	exc := ev.Exceptions[0]
	if exc.Type != "RuntimeException" || exc.Value != "Checkout failed" {
		t.Errorf("exception = %q %q, want RuntimeException \"Checkout failed\"", exc.Type, exc.Value)
	}
	if len(exc.Frames) == 0 {
		t.Fatal("no stack frames")
	}
	var inApp []sentry.Frame
	for _, f := range exc.Frames {
		if f.InApp {
			inApp = append(inApp, f)
		}
	}
	if len(inApp) == 0 {
		t.Fatal("no in-app frame")
	}
	if len(inApp) == len(exc.Frames) {
		t.Error("every frame is in-app; the vendor frames should not be")
	}
	top := exc.Frames[len(exc.Frames)-1]
	if top.Filename != "/app/Services/PaymentGateway.php" || top.Lineno != 12 || !top.InApp {
		t.Errorf("top frame = %s:%d in_app=%v", top.Filename, top.Lineno, top.InApp)
	}
	if top.Function != `App\Services\PaymentGateway::charge` {
		t.Errorf("top frame function = %q", top.Function)
	}
	if !strings.Contains(top.ContextLine, "Checkout failed") || len(top.PreContext) == 0 || len(top.PostContext) == 0 {
		t.Errorf("top frame context = %q / %d pre / %d post", top.ContextLine, len(top.PreContext), len(top.PostContext))
	}

	if ev.Platform != "php" || ev.Release != "webshop@1.4.2" || ev.Environment != "production" || ev.ServerName != "web-1" {
		t.Errorf("platform/release/environment/server = %q %q %q %q", ev.Platform, ev.Release, ev.Environment, ev.ServerName)
	}
	if ev.Level != "error" {
		t.Errorf("Level = %q, want error (the default when the SDK sends none)", ev.Level)
	}
	if want := time.Date(2026, 10, 16, 11, 56, 49, 783_405_000, time.UTC); ev.Timestamp.Sub(want).Abs() > time.Millisecond {
		t.Errorf("Timestamp = %v, want about %v", ev.Timestamp, want)
	}
	if ev.User.ID != "42" {
		t.Errorf("User.ID = %q", ev.User.ID)
	}
	if ev.SDK.Name != "sentry.php" || ev.SDK.Version != "4.34.0" {
		t.Errorf("SDK = %+v", ev.SDK)
	}
	if ev.Request.Method != "POST" || !strings.HasPrefix(ev.Request.URL, "http://localhost:18091/checkout") {
		t.Errorf("Request = %s %s", ev.Request.Method, ev.Request.URL)
	}
	// PHP sends header values as lists.
	if ua := ev.Request.Headers["User-Agent"]; !strings.HasPrefix(ua, "Mozilla/5.0") {
		t.Errorf("User-Agent header = %q", ua)
	}
	if ev.Tags["checkout_step"] != "payment" {
		t.Errorf("Tags = %v", ev.Tags)
	}
	if ev.Contexts["runtime"]["name"] != "php" {
		t.Errorf("Contexts[runtime] = %v", ev.Contexts["runtime"])
	}
}

func TestParseEnvelope_BrowserFixture(t *testing.T) {
	env := parse(t, fixture(t, "browser-typeerror.envelope"))

	if len(env.Events) != 1 {
		t.Fatalf("got %d events, want 1", len(env.Events))
	}
	ev := env.Events[0]
	if ev.EventID != "3df5f8fa0ad34f87b4a3b7109b9d93db" {
		t.Errorf("EventID = %q", ev.EventID)
	}
	if len(ev.Exceptions) != 1 {
		t.Fatalf("got %d exceptions, want 1", len(ev.Exceptions))
	}
	exc := ev.Exceptions[0]
	if exc.Type != "TypeError" || exc.Value != "Cannot read properties of undefined (reading 'toFixed')" {
		t.Errorf("exception = %q %q", exc.Type, exc.Value)
	}
	if exc.Mechanism.Handled == nil || *exc.Mechanism.Handled {
		t.Errorf("Mechanism.Handled = %v, want false", exc.Mechanism.Handled)
	}
	if len(exc.Frames) == 0 {
		t.Fatal("no stack frames")
	}
	top := exc.Frames[len(exc.Frames)-1]
	if top.Function != "renderCartTotal" || !top.InApp || top.Lineno == 0 || top.Colno == 0 {
		t.Errorf("top frame = %+v", top)
	}
	if !strings.HasSuffix(top.Filename, "/assets/index-D8xk2Lp1.js") {
		t.Errorf("top frame filename = %q", top.Filename)
	}
	if ev.Platform != "javascript" || ev.Level != "error" || ev.SDK.Name != "sentry.javascript.browser" {
		t.Errorf("platform/level/sdk = %q %q %q", ev.Platform, ev.Level, ev.SDK.Name)
	}
	if ev.User.ID != "42" || !strings.HasPrefix(ev.Request.URL, "http://localhost:5173/checkout/payment") {
		t.Errorf("user/url = %q %q", ev.User.ID, ev.Request.URL)
	}
}

func TestParseEnvelope_SessionFixtureHasNoEvents(t *testing.T) {
	env := parse(t, fixture(t, "browser-session.envelope"))

	if len(env.Events) != 0 || env.Skipped != 1 {
		t.Errorf("events = %d, skipped = %d; want 0 and 1", len(env.Events), env.Skipped)
	}
}

func TestParseEnvelope_SkipsOtherItemTypes(t *testing.T) {
	attachment := "line one\nline two\n{\"type\":\"event\"}\n" + string([]byte{0, 1, 2, '\n', 0xff})
	body := `{"event_id":"9ec79c33ec9942ab8353589fcb2e04dc","sent_at":"2026-10-16T11:42:26Z"}` + "\n" +
		item(`{"type":"client_report"}`, `{"timestamp":1792150946,"discarded_events":[{"reason":"queue_overflow","category":"error","quantity":1}]}`, false) +
		item(`{"type":"session"}`, `{"sid":"cc17f8508fc447e39ba7f923d559916c","status":"ok"}`, true) +
		item(`{"type":"transaction"}`, `{"type":"transaction","transaction":"/checkout","spans":[]}`, true) +
		item(`{"type":"attachment","filename":"log.txt"}`, attachment, true) +
		item(`{"type":"event"}`, minimalEvent, false)

	env := parse(t, []byte(body))

	if len(env.Events) != 1 {
		t.Fatalf("got %d events, want 1", len(env.Events))
	}
	if env.Events[0].Exceptions[0].Type != "LogicException" {
		t.Errorf("event = %+v", env.Events[0])
	}
	if env.Skipped != 4 {
		t.Errorf("Skipped = %d, want 4", env.Skipped)
	}
}

func TestParseEnvelope_LengthAndNewlineItems(t *testing.T) {
	pretty := "{\n  \"event_id\": \"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\",\n  \"message\": \"pretty\"\n}"
	cases := []struct {
		name string
		body string
		want []string // event ids, in order
	}{
		{
			name: "event with length",
			body: "{}\n" + item(`{"type":"event"}`, minimalEvent, true),
			want: []string{"9ec79c33ec9942ab8353589fcb2e04dc"},
		},
		{
			name: "event without length",
			body: "{}\n" + item(`{"type":"event"}`, minimalEvent, false),
			want: []string{"9ec79c33ec9942ab8353589fcb2e04dc"},
		},
		{
			name: "no newline at the end",
			body: "{}\n" + `{"type":"event"}` + "\n" + minimalEvent,
			want: []string{"9ec79c33ec9942ab8353589fcb2e04dc"},
		},
		{
			name: "length item with newlines inside, then a newline-delimited event",
			body: "{}\n" + item(`{"type":"event"}`, pretty, true) + item(`{"type":"event"}`, minimalEvent, false),
			want: []string{"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "9ec79c33ec9942ab8353589fcb2e04dc"},
		},
		{
			name: "empty items with and without length",
			body: "{}\n" + item(`{"type":"attachment"}`, "", true) + item(`{"type":"client_report"}`, "", false) +
				item(`{"type":"event"}`, minimalEvent, true),
			want: []string{"9ec79c33ec9942ab8353589fcb2e04dc"},
		},
		{
			name: "length payload ends at EOF without newline",
			body: "{}\n" + strings.TrimSuffix(item(`{"type":"event"}`, minimalEvent, true), "\n"),
			want: []string{"9ec79c33ec9942ab8353589fcb2e04dc"},
		},
		{
			name: "only the envelope header",
			body: `{"event_id":"9ec79c33ec9942ab8353589fcb2e04dc"}`,
		},
		{
			name: "event id from the envelope header when the event has none",
			body: `{"event_id":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}` + "\n" + item(`{"type":"event"}`, `{"message":"hi"}`, false),
			want: []string{"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := parse(t, []byte(tc.body))
			var got []string
			for _, ev := range env.Events {
				got = append(got, ev.EventID)
			}
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Errorf("event ids = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestParseEnvelope_DropsLargeEvents(t *testing.T) {
	big := `{"message":"` + strings.Repeat("x", 2000) + `"}`
	for _, withLength := range []bool{true, false} {
		t.Run(fmt.Sprintf("length=%v", withLength), func(t *testing.T) {
			body := "{}\n" + item(`{"type":"event"}`, big, withLength) + item(`{"type":"event"}`, minimalEvent, withLength)

			env, err := sentry.ParseEnvelope(strings.NewReader(body), 1000)
			if err != nil {
				t.Fatalf("ParseEnvelope: %v", err)
			}
			if env.Dropped != 1 || len(env.Events) != 1 || env.Events[0].EventID != "9ec79c33ec9942ab8353589fcb2e04dc" {
				t.Errorf("dropped = %d, events = %d; want the large event dropped and the small one kept", env.Dropped, len(env.Events))
			}
		})
	}
}

func TestParseEnvelope_Malformed(t *testing.T) {
	cases := map[string]string{
		"empty":                       "",
		"blank line":                  "\n",
		"header is not JSON":          "{nope\n" + item(`{"type":"event"}`, minimalEvent, false),
		"header is not an object":     "[]\n",
		"item header is not JSON":     "{}\n{type:event}\n" + minimalEvent,
		"item header has no type":     "{}\n" + item(`{"length":2}`, "{}", false),
		"negative length":             "{}\n" + `{"type":"event","length":-1}` + "\n" + minimalEvent,
		"length longer than the body": "{}\n" + `{"type":"event","length":500}` + "\n" + minimalEvent,
		"skipped item truncated":      "{}\n" + `{"type":"attachment","length":500}` + "\n" + "short",
		"junk after a length payload": "{}\n" + `{"type":"event","length":2}` + "\n" + "{}junk\n",
		"truncated event JSON":        "{}\n" + item(`{"type":"event"}`, minimalEvent[:40], false),
		"event payload is not object": "{}\n" + item(`{"type":"event"}`, `[1,2,3]`, false),
		"event payload missing":       "{}\n" + `{"type":"event"}`,
		"header line too long":        `{"x":"` + strings.Repeat("a", 100_000) + `"}` + "\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := sentry.ParseEnvelope(strings.NewReader(body), maxItem)
			if !errors.Is(err, sentry.ErrBadEnvelope) {
				t.Errorf("err = %v, want ErrBadEnvelope", err)
			}
		})
	}
}

func TestParseEnvelope_ReadErrorIsKept(t *testing.T) {
	boom := errors.New("connection reset")
	r := io.MultiReader(strings.NewReader("{}\n"+`{"type":"event"}`+"\n{"), &failingReader{err: boom})

	_, err := sentry.ParseEnvelope(r, maxItem)
	if !errors.Is(err, sentry.ErrBadEnvelope) || !errors.Is(err, boom) {
		t.Errorf("err = %v, want ErrBadEnvelope wrapping the read error", err)
	}
}

type failingReader struct{ err error }

func (f *failingReader) Read([]byte) (int, error) { return 0, f.err }

func FuzzParseEnvelope(f *testing.F) {
	f.Add(fixture(f, "php-exception.envelope.gz"))
	f.Add(fixture(f, "browser-typeerror.envelope"))
	f.Add(fixture(f, "browser-session.envelope"))
	f.Add([]byte("{}\n" + item(`{"type":"event"}`, minimalEvent, true)))
	f.Add([]byte("{}\n" + item(`{"type":"attachment"}`, "a\nb", true) + item(`{"type":"event"}`, minimalEvent, false)))
	f.Add([]byte(`{}` + "\n" + `{"type":"event"}` + "\n" + `{"exception":[{"type":1,"stacktrace":{"frames":[null,{"in_app":"yes"}]}}],"request":{"headers":[["a"]],"query_string":[[1,2]]},"breadcrumbs":{"values":[null,{"data":{"url":5}}]}}`))
	f.Add([]byte(""))

	f.Fuzz(func(t *testing.T, data []byte) {
		env, err := sentry.ParseEnvelope(bytes.NewReader(data), 64*1024)
		if err != nil {
			if !errors.Is(err, sentry.ErrBadEnvelope) {
				t.Fatalf("error does not wrap ErrBadEnvelope: %v", err)
			}
			return
		}
		// Everything ingest does with a parsed event must cope with whatever was sent.
		for _, ev := range env.Events {
			scrub.Event(ev)
			sentry.Fingerprint(ev)
			summary := sentry.Summarize(ev)
			sentry.Source(ev)
			// What the writer puts in columns, Postgres must take: no NUL,
			// valid UTF-8, and indexed values short enough.
			for _, v := range []string{summary.Title, summary.Message, summary.Culprit, ev.User.ID, ev.Release,
				ev.Environment, ev.Platform, ev.Level, ev.Request.URL} {
				if strings.ContainsRune(v, 0) || !utf8.ValidString(v) {
					t.Fatalf("a value for a column is not text Postgres stores: %q", v)
				}
			}
			if len(ev.User.ID) > 200 || len(ev.Release) > 200 || len(ev.Environment) > 200 {
				t.Fatalf("user id, release or environment longer than 200 bytes")
			}
			data, err := ev.JSON()
			if err != nil {
				t.Fatalf("JSON: %v", err)
			}
			if !json.Valid(data) {
				t.Fatalf("JSON is not valid: %s", data)
			}
		}
	})
}

func TestFixtures_HaveNoMachineStrings(t *testing.T) {
	// Recordings must be sanitized before they are committed (see
	// testdata/capture/sanitize.mjs): no local paths, user or host names.
	machine := []string{"/private/", "/Users/", "/home/", "claude-", "scratchpad", "Darwin Kernel", "xnu-", ".home "}
	files, err := filepath.Glob(filepath.Join("testdata", "*"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range files {
		if info, err := os.Stat(path); err != nil || info.IsDir() {
			continue
		}
		data := string(fixture(t, filepath.Base(path)))
		unescaped := strings.ReplaceAll(data, `\/`, "/") // PHP writes "/" as "\/"
		for _, s := range machine {
			if strings.Contains(unescaped, s) {
				t.Errorf("%s contains %q; sanitize the recording", path, s)
			}
		}
	}
}
