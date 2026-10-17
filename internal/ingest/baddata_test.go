package ingest

import (
	"net/http"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// Values Postgres refuses (a NUL character anywhere in a text column; an
// index entry over 2704 bytes, such as a 6 KB user id) arrive from real
// apps: a Laravel QueryException whose message holds a binary UUID binding,
// a user id that is a whole serialized object. They are cleaned when the
// event is read, so the event is stored, and the other events of the batch
// (other projects too) are stored with it, on both databases.
func TestWriter_StoresEventsWithValuesPostgresRefusesClean(t *testing.T) {
	env := newTestEnv(t, 100)
	webshop := env.project(t, "Webshop", 1000)
	portal := env.project(t, "Customer portal", 1000)

	sentryEvent := func(eventID, extra string) []byte {
		event := `{"event_id":"` + eventID + `","platform":"php","timestamp":1792152010,` +
			`"exception":{"values":[{"type":"Illuminate\\Database\\QueryException","value":"SQLSTATE[23000]: binding 0x\u0000\u0011\u00e9",` +
			`"stacktrace":{"frames":[{"filename":"/app/Services/PaymentGateway.php","function":"charge","lineno":12,"in_app":true}]}}]},` +
			`"request":{"url":"https://portal.example.com/orders/a%00b"}` + extra + `}`
		return []byte(`{"event_id":"` + eventID + `"}` + "\n" + `{"type":"event"}` + "\n" + event + "\n")
	}
	longID := strings.Repeat("u", 6000)
	for _, req := range []struct {
		key, path string
		body      []byte
	}{
		{webshop.SentryKey, envelopePath(webshop), envelope(eventIDN(1), "")},
		{portal.SentryKey, envelopePath(portal), sentryEvent(eventIDN(2), `,"user":{"id":"anna\u0000"},"release":"portal@1.0\u0000"`)},
		{portal.SentryKey, envelopePath(portal), sentryEvent(eventIDN(3),
			`,"user":{"id":"`+longID+`"},"release":"`+strings.Repeat("r", 300)+`","environment":"`+strings.Repeat("é", 150)+`"`)},
	} {
		if rec := env.post(req.path, req.body, authHeader(req.key)); rec.Code != http.StatusOK {
			t.Fatalf("envelope = %d %s", rec.Code, rec.Body)
		}
	}
	posthogEvent := func(token, uuid, name, user, url, props string) string {
		return `{"event":"` + name + `","uuid":"` + uuid + `","timestamp":"2026-10-16T12:00:05Z","properties":{"distinct_id":"` + user +
			`","$current_url":"` + url + `","$session_id":"s1\u0000","$window_id":"w1","token":"` + token + `"` + props + `}}`
	}
	body := `{"sent_at":"2026-10-16T12:00:10Z","batch":[` +
		posthogEvent(webshop.PosthogToken, "v1", "$pageview", "42", "https://shop.example.com/checkout", "") + `,` +
		posthogEvent(webshop.PosthogToken, "c1", "checkout_started", "42", "https://shop.example.com/checkout", `,"plan":"pro"`) + `,` +
		posthogEvent(portal.PosthogToken, `v2\u0000`, "$pageview", `anna\u0000`, `https://portal.example.com/a%00b/c\u0000d`, "") + `,` +
		posthogEvent(portal.PosthogToken, "c2", `signed_up\u0000`, `anna\u0000`, "https://portal.example.com/welcome",
			`,"pl\u0000an":"pro\u0000","nested":["a\u0000"]`) +
		`]}`
	if rec := env.capture("/batch/", body); rec.Code != http.StatusOK {
		t.Fatalf("capture = %d %s", rec.Code, rec.Body)
	}

	// One flush writes it all: the error events of both projects in one
	// part, the page events in another, the custom events in a third.
	w := NewWriter(env.db, env.queue, &recordingHooks{}, env.logger)
	w.retryDelays = []time.Duration{time.Millisecond}
	w.Close()

	if st := w.Stats(); st.LostEvents != 0 {
		t.Errorf("lost %d events (%s)", st.LostEvents, st.LastError)
	}
	db := env.rawDB(t)
	count := func(query string, args ...any) int {
		t.Helper()
		var n int
		if err := db.QueryRow(query, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	for _, table := range []string{"error_events", "pageviews", "custom_events"} {
		if n := count("SELECT COUNT(*) FROM "+table+" WHERE project_id = $1", webshop.ID); n != 1 {
			t.Errorf("Webshop has %d rows in %s, want 1: a bad event of another project lost them", n, table)
		}
	}

	rows, err := db.Query(`SELECT event_id, user_id, release, environment, url, route, data FROM error_events
		WHERE project_id = $1 ORDER BY event_id`, portal.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var stored int
	for rows.Next() {
		var id, user, release, environment, url, route, data string
		if err := rows.Scan(&id, &user, &release, &environment, &url, &route, &data); err != nil {
			t.Fatal(err)
		}
		stored++
		for name, value := range map[string]string{"user_id": user, "release": release, "environment": environment, "url": url, "route": route, "data": data} {
			if strings.ContainsRune(value, 0) || strings.Contains(value, `\u0000`) {
				t.Errorf("event %s: %s keeps a NUL", id, name)
			}
			if !utf8.ValidString(value) {
				t.Errorf("event %s: %s is not UTF-8", id, name)
			}
		}
		switch id {
		case eventIDN(2):
			if user != "anna" || release != "portal@1.0" || route != "/orders/a%00b" {
				t.Errorf("event 2: user %q, release %q, route %q; want anna, portal@1.0, /orders/a%%00b", user, release, route)
			}
		case eventIDN(3):
			if len(user) != 200 || len(release) != 200 || len(environment) != 200 || !utf8.ValidString(environment) {
				t.Errorf("event 3: user %d bytes, release %d, environment %d; want 200 each, whole characters",
					len(user), len(release), len(environment))
			}
			if !strings.Contains(data, `"id":"`+user+`"`) || strings.Contains(data, longID) {
				t.Errorf("event 3: the stored event keeps another user id than the column")
			}
		}
	}
	if stored != 2 {
		t.Errorf("Customer portal stored %d error events, want 2", stored)
	}
	var title, message string
	if err := db.QueryRow("SELECT title, message FROM issues WHERE project_id = $1", portal.ID).Scan(&title, &message); err != nil {
		t.Fatal(err)
	}
	if message != "SQLSTATE[23000]: binding 0x\u0011é" {
		t.Errorf("issue message = %q, want it without the NUL", message)
	}

	var user, path, session, pageviewID string
	if err := db.QueryRow("SELECT user_id, path, session_id, pageview_id FROM pageviews WHERE project_id = $1", portal.ID).
		Scan(&user, &path, &session, &pageviewID); err != nil {
		t.Fatalf("Customer portal's page view: %v", err)
	}
	if user != "anna" || path != "/a%00b/cd" || session != "s1" || pageviewID != "v2" {
		t.Errorf("page view = user %q, path %q, session %q, id %q; want anna, /a%%00b/cd, s1, v2", user, path, session, pageviewID)
	}
	customs := customEventRows(t, env, portal.ID)
	if len(customs) != 1 {
		t.Fatalf("Customer portal stored %d custom events, want 1", len(customs))
	}
	if c := customs[0]; c.name != "signed_up" || c.user != "anna" || c.properties["plan"] != "pro" ||
		len(c.properties["nested"].([]any)) != 1 || c.properties["nested"].([]any)[0] != "a" {
		t.Errorf("custom event = %+v", c)
	}
	if strings.Contains(env.logs.String(), "level=ERROR") {
		t.Errorf("errors logged:\n%s", env.logs.String())
	}
}
