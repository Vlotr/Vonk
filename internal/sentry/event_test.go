package sentry_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"monitor/internal/sentry"
)

func decode(t *testing.T, data string) *sentry.Event {
	t.Helper()
	ev, err := sentry.DecodeEvent([]byte(data))
	if err != nil {
		t.Fatalf("DecodeEvent: %v", err)
	}
	return ev
}

func TestDecodeEvent_LenientShapes(t *testing.T) {
	// Older SDKs and other languages send many fields in a second shape.
	ev := decode(t, `{
		"event_id": "9EC79C33-EC99-42AB-8353-589FCB2E04DC",
		"timestamp": "2026-10-16T11:42:26.5Z",
		"level": "warning",
		"exception": [{"type": "ValueError", "value": "bad", "module": "app.views",
			"stacktrace": {"frames": [{"filename": "app/views.py", "lineno": "7", "function": "index", "in_app": true}]}}],
		"logentry": {"message": "User %s failed", "params": ["jan"], "formatted": "User jan failed"},
		"tags": [["browser", "Chrome"], ["count", 3]],
		"request": {
			"url": "https://shop.example.com/cart",
			"headers": [["User-Agent", "curl/8"], ["Accept", "*/*"]],
			"query_string": {"page": "2"},
			"env": {"SERVER_NAME": "web-1"}
		},
		"user": {"id": 42, "username": "jan"},
		"fingerprint": ["checkout", 1],
		"debug_meta": {"images": [{"type": "sourcemap", "code_file": "https://shop.example.com/app.js", "debug_id": "abc"}]}
	}`)

	if ev.EventID != "9ec79c33ec9942ab8353589fcb2e04dc" {
		t.Errorf("EventID = %q, want lowercase without dashes", ev.EventID)
	}
	if want := time.Date(2026, 10, 16, 11, 42, 26, 500_000_000, time.UTC); !ev.Timestamp.Equal(want) {
		t.Errorf("Timestamp = %v, want %v", ev.Timestamp, want)
	}
	if ev.Level != "warning" {
		t.Errorf("Level = %q", ev.Level)
	}
	if len(ev.Exceptions) != 1 || ev.Exceptions[0].Type != "ValueError" || ev.Exceptions[0].Module != "app.views" {
		t.Fatalf("Exceptions = %+v", ev.Exceptions)
	}
	if f := ev.Exceptions[0].Frames; len(f) != 1 || f[0].Lineno != 7 || !f[0].InApp || f[0].Function != "index" {
		t.Errorf("Frames = %+v", f)
	}
	if ev.Message != "User jan failed" {
		t.Errorf("Message = %q, want the formatted logentry", ev.Message)
	}
	if ev.Tags["browser"] != "Chrome" || ev.Tags["count"] != "3" {
		t.Errorf("Tags = %v", ev.Tags)
	}
	if ev.Request.Headers["User-Agent"] != "curl/8" || ev.Request.QueryString != "page=2" || ev.Request.Env["SERVER_NAME"] != "web-1" {
		t.Errorf("Request = %+v", ev.Request)
	}
	if ev.User.ID != "42" || ev.User.Username != "jan" {
		t.Errorf("User = %+v", ev.User)
	}
	if strings.Join(ev.Fingerprint, "|") != "checkout|1" {
		t.Errorf("Fingerprint = %v", ev.Fingerprint)
	}
	if len(ev.DebugImages) != 1 || ev.DebugImages[0].Type != "sourcemap" || ev.DebugImages[0].DebugID != "abc" {
		t.Errorf("DebugImages = %+v", ev.DebugImages)
	}
}

func TestDecodeEvent_MessageShapes(t *testing.T) {
	cases := map[string]string{
		`{"message": "plain"}`: "plain",
		`{"message": {"formatted": "formatted", "message": "raw %s"}}`:   "formatted",
		`{"message": {"message": "raw"}}`:                                "raw",
		`{"logentry": {"message": "from logentry"}, "message": "other"}`: "from logentry",
	}
	for in, want := range cases {
		if got := decode(t, in).Message; got != want {
			t.Errorf("%s: Message = %q, want %q", in, got, want)
		}
	}
}

func TestDecodeEvent_Size(t *testing.T) {
	data := `{"event_id":"5f2b6a0c7d3e4f1a8b9c0d1e2f3a4b5c","message":"hello"}`
	if ev := decode(t, data); ev.Size != len(data) {
		t.Errorf("Size = %d, want %d (the bytes it was decoded from)", ev.Size, len(data))
	}
}

func TestDecodeEvent_UnixTimestamp(t *testing.T) {
	ev := decode(t, `{"timestamp": 1792150946.5}`)
	if want := time.Unix(1792150946, 500_000_000).UTC(); ev.Timestamp.Sub(want).Abs() > time.Millisecond {
		t.Errorf("Timestamp = %v, want %v", ev.Timestamp, want)
	}
	if ev := decode(t, `{"timestamp": "yesterday"}`); !ev.Timestamp.IsZero() {
		t.Errorf("unreadable timestamp = %v, want zero", ev.Timestamp)
	}
}

func TestDecodeEvent_WrongTypesAreIgnored(t *testing.T) {
	ev := decode(t, `{"exception": {"values": [null, 5, {"type": ["x"], "stacktrace": {"frames": "none"}}]},
		"request": "nope", "user": [], "tags": 7, "contexts": {"os": "macOS", "runtime": {"name": "php"}}, "level": 3}`)

	if len(ev.Exceptions) != 1 || ev.Exceptions[0].Type != "" || len(ev.Exceptions[0].Frames) != 0 {
		t.Errorf("Exceptions = %+v", ev.Exceptions)
	}
	if ev.Level != "error" {
		t.Errorf("Level = %q, want the default error", ev.Level)
	}
	if ev.Contexts["runtime"]["name"] != "php" || ev.Contexts["os"] != nil {
		t.Errorf("Contexts = %v", ev.Contexts)
	}
}

func TestDecodeEvent_RejectsNonObjects(t *testing.T) {
	for _, in := range []string{``, `null`, `[]`, `"x"`, `{"a":1`, `{"a":1} {"b":2}`} {
		if _, err := sentry.DecodeEvent([]byte(in)); err == nil {
			t.Errorf("DecodeEvent(%q): want error, got nil", in)
		}
	}
}

func TestEventJSON_KeepsEverythingAsSent(t *testing.T) {
	in := `{"event_id":"9ec79c33ec9942ab8353589fcb2e04dc","new_sdk_field":{"nested":[1,2.50,true]},"big":12345678901234567890}`
	ev := decode(t, in)

	out, err := ev.JSON()
	if err != nil {
		t.Fatal(err)
	}
	var got, want map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	json.Unmarshal([]byte(in), &want)
	if !strings.Contains(string(out), "12345678901234567890") || !strings.Contains(string(out), "2.50") {
		t.Errorf("numbers were rewritten: %s", out)
	}
	if got["new_sdk_field"] == nil {
		t.Errorf("unknown field lost: %s", out)
	}
}

func TestEvent_SetEventID(t *testing.T) {
	ev := decode(t, `{"message":"hi"}`)
	ev.SetEventID("cccccccccccccccccccccccccccccccc")

	out, _ := ev.JSON()
	if ev.EventID != "cccccccccccccccccccccccccccccccc" || !strings.Contains(string(out), `"event_id":"cccccccccccccccccccccccccccccccc"`) {
		t.Errorf("EventID = %q, JSON = %s", ev.EventID, out)
	}
}

// Postgres refuses a NUL in any text column, and an index entry over 2704
// bytes, so one such event would fail the write of its whole batch. The
// event is cleaned as it is read: every string of it loses its NULs (the
// stored JSON too, so nothing read back from it brings one), and the user
// id (indexed), release and environment are cut to 200 bytes, in the
// typed fields and in Raw alike.
func TestDecodeEvent_CleansValuesForTheDatabase(t *testing.T) {
	long := strings.Repeat("u", 6000)
	ev := decode(t, `{"event_id":"9ec79c33ec9942ab8353589fcb2e04dc","platform":"php\u0000",
		"message":"failed\u0000",
		"exception":{"values":[{"type":"Query\u0000Exception","value":"binding 0x\u0000\u0011é",
			"stacktrace":{"frames":[{"filename":"/app/a\u0000.php","function":"run","context_line":"x\u0000"}]}}]},
		"user":{"id":"`+long+`","email":"anna\u0000@example.com"},
		"release":"`+strings.Repeat("r", 300)+`","environment":"`+strings.Repeat("é", 150)+`",
		"request":{"url":"https://shop.example.com/a\u0000b","headers":{"X-Ke\u0000y":"v\u0000"}},
		"extra":{"nested":[{"deep\u0000":"\u0000value"}]}}`)

	if ev.Platform != "php" || ev.Message != "failed" || ev.Request.URL != "https://shop.example.com/ab" {
		t.Errorf("typed fields keep a NUL: platform %q, message %q, url %q", ev.Platform, ev.Message, ev.Request.URL)
	}
	if exc := ev.Exceptions[0]; exc.Type != "QueryException" || exc.Value != "binding 0x\u0011é" || exc.Frames[0].Filename != "/app/a.php" {
		t.Errorf("exception = %+v", exc)
	}
	if ev.User.Email != "anna@example.com" || ev.Request.Headers["X-Key"] != "v" {
		t.Errorf("user email %q, headers %v", ev.User.Email, ev.Request.Headers)
	}
	if len(ev.User.ID) != 200 || len(ev.Release) != 200 || len(ev.Environment) != 200 || ev.Environment != strings.Repeat("é", 100) {
		t.Errorf("user id %d bytes, release %d, environment %d; want 200 each", len(ev.User.ID), len(ev.Release), len(ev.Environment))
	}
	data, err := ev.JSON()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), `\u0000`) || strings.Contains(string(data), long) {
		t.Errorf("stored JSON keeps a NUL or the whole user id")
	}
	var back map[string]any
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	if user := back["user"].(map[string]any); user["id"] != ev.User.ID || back["release"] != ev.Release || back["environment"] != ev.Environment {
		t.Errorf("stored JSON and typed fields differ: user %v, release %v", user["id"], back["release"])
	}
	if !strings.Contains(string(data), `{"deep":"value"}`) {
		t.Errorf("nested values keep their NUL: %s", data)
	}

	// Anything that changes Raw afterwards (scrub, symbolication) calls
	// Refresh, which cleans again.
	ev.Raw["message"] = "again\x00"
	ev.Raw["user"] = map[string]any{"id": json.Number(strings.Repeat("9", 300))}
	ev.Refresh()
	if ev.Message != "again" || ev.User.ID != strings.Repeat("9", 200) {
		t.Errorf("after Refresh: message %q, user id %d bytes", ev.Message, len(ev.User.ID))
	}
}
