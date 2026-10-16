package ingest

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

type customEventRow struct {
	name, user, path, route string
	at                      time.Time
	bot                     bool
	properties              map[string]any
}

func customEventRows(t *testing.T, env *testEnv, projectID int64) []customEventRow {
	t.Helper()
	rows, err := env.rawDB(t).Query(`SELECT name, user_id, path, route, at, is_bot, properties FROM custom_events
		WHERE project_id = $1 ORDER BY at, id`, projectID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []customEventRow
	for rows.Next() {
		var r customEventRow
		var at int64
		var props string
		if err := rows.Scan(&r.name, &r.user, &r.path, &r.route, &at, &r.bot, &props); err != nil {
			t.Fatal(err)
		}
		r.at = time.UnixMilli(at).UTC()
		if err := json.Unmarshal([]byte(props), &r.properties); err != nil {
			t.Fatalf("properties %q: %v", props, err)
		}
		out = append(out, r)
	}
	return out
}

// writeQueue writes everything queued, as the writer does.
func (e *testEnv) writeQueue() {
	NewWriter(e.db, e.queue, &recordingHooks{}, e.logger).Close()
}

// The app's own events (names without "$") are stored once per uuid, with
// their page, user and properties, scrubbed (no passwords, no IP
// addresses, none of the SDK's "$" properties), and summarized per hour:
// how often, by how many users, bots apart.
func TestCustomEvents_StoredAndAggregated(t *testing.T) {
	env := newTestEnv(t, 100)
	p := env.project(t, "Webshop", 1000)
	p = env.setRoutePatterns(t, p, "/products/:slug")
	event := func(uuid, name, user, url string, extra string) string {
		return `{"event":"` + name + `","uuid":"` + uuid + `","timestamp":"2026-10-16T12:00:05Z","properties":{"distinct_id":"` + user +
			`","$current_url":"` + url + `","$ip":"203.0.113.9","$browser":"Chrome","token":"` + p.PosthogToken + `"` + extra + `}}`
	}
	body := `{"api_key":"` + p.PosthogToken + `","sent_at":"2026-10-16T12:00:10Z","batch":[` +
		event("e1", "checkout_completed", "42", "https://shop.example.com/checkout?token=tok_abc123",
			`,"plan":"pro","total":49.95,"password":"hunter2","ip":"198.51.100.7","utm_medium":null,"nested":{"api_key":"k","color":"blue"}`) + `,` +
		event("e1", "checkout_completed", "42", "https://shop.example.com/checkout", "") + `,` + // sent twice
		event("e2", "checkout_completed", "7", "https://shop.example.com/checkout", "") + `,` +
		event("e3", "product_viewed", "42", "https://shop.example.com/products/blue-shoes", `,"product":"blue-shoes"`) + `]}`
	if rec := env.capture("/batch/", body); rec.Code != http.StatusOK {
		t.Fatalf("capture = %d %s", rec.Code, rec.Body)
	}
	// A bot's event, by the User-Agent of its request.
	if rec := env.capture("/i/v0/e/", `{"api_key":"`+p.PosthogToken+`","batch":[`+event("e4", "checkout_completed", "bot-1", "https://shop.example.com/checkout", "")+`]}`,
		func(r *http.Request) {
			r.Header.Set("User-Agent", "Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)")
		}); rec.Code != http.StatusOK {
		t.Fatalf("bot capture = %d", rec.Code)
	}
	env.writeQueue()

	rows := customEventRows(t, env, p.ID)
	if len(rows) != 4 {
		t.Fatalf("stored %d custom events, want 4 (one per uuid): %+v", len(rows), rows)
	}
	first := rows[0]
	if first.name != "checkout_completed" || first.user != "42" || first.path != "/checkout" || first.route != "/checkout" || first.bot {
		t.Errorf("first event = %+v", first)
	}
	// Received at 12:00:10 (the test clock), sent at 12:00:10 by the client's clock, happened 5 s before.
	if !first.at.Equal(time.Date(2026, 10, 16, 12, 0, 5, 0, time.UTC)) {
		t.Errorf("at = %v, want 12:00:05", first.at)
	}
	props, _ := json.Marshal(first.properties)
	if first.properties["plan"] != "pro" || first.properties["total"] != 49.95 || first.properties["password"] != "[Filtered]" {
		t.Errorf("properties = %s", props)
	}
	for _, gone := range []string{"$ip", "$browser", "$current_url", "ip", "token", "distinct_id", "utm_medium", "203.0.113.9", "198.51.100.7", "tok_abc123", `"k"`} {
		if strings.Contains(string(props), gone) {
			t.Errorf("properties keep %s: %s", gone, props)
		}
	}
	if nested, _ := first.properties["nested"].(map[string]any); nested["color"] != "blue" || nested["api_key"] != "[Filtered]" {
		t.Errorf("nested = %v", first.properties["nested"])
	}
	if rows[2].route != "/products/:slug" || rows[2].path != "/products/blue-shoes" || rows[2].properties["product"] != "blue-shoes" {
		t.Errorf("product event = %+v", rows[2])
	}
	if !rows[3].bot {
		t.Errorf("the Googlebot event is not marked as a bot: %+v", rows[3])
	}

	// The hour is marked and summarized.
	ctx := context.Background()
	hours, err := env.db.DirtyHours(ctx)
	if err != nil || len(hours) != 1 {
		t.Fatalf("dirty hours = %v, %v; want 12:00", hours, err)
	}
	if err := env.db.RecomputeHour(ctx, p.ID, hours[0].Hour); err != nil {
		t.Fatal(err)
	}
	stats, err := env.db.EventStats(ctx, p.ID, hours[0].Hour, hours[0].Hour.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(stats.Events) != 2 || stats.Bots != 1 {
		t.Fatalf("event stats = %+v", stats)
	}
	if e := stats.Events[0]; e.Name != "checkout_completed" || e.Events != 2 || e.Users != 2 {
		t.Errorf("checkout_completed = %+v, want 2 events by 2 users", e)
	}
	if e := stats.Events[1]; e.Name != "product_viewed" || e.Events != 1 || e.Users != 1 {
		t.Errorf("product_viewed = %+v", e)
	}
}

// Events whose name starts with "$" are PostHog's own (identify, autocapture,
// feature flags, web vitals, ...): accepted and counted against the limit,
// never stored as custom events.
func TestCustomEvents_SystemEventsSkipped(t *testing.T) {
	env := newTestEnv(t, 100)
	p := env.project(t, "Webshop", 1000)
	var events []string
	for _, name := range []string{"$identify", "$autocapture", "$feature_flag_called", "$web_vitals", "$set", "$exception", "$groupidentify", "$opt_in"} {
		events = append(events, `{"event":"`+name+`","properties":{"distinct_id":"42","$current_url":"https://shop.example.com/"}}`)
	}
	if rec := env.capture("/batch/", `{"api_key":"`+p.PosthogToken+`","batch":[`+strings.Join(events, ",")+`]}`); rec.Code != http.StatusOK {
		t.Fatalf("capture = %d %s", rec.Code, rec.Body)
	}
	if n := env.queue.Len(); n != 0 {
		t.Errorf("%d system events queued, want none", n)
	}
	env.writeQueue()
	if rows := customEventRows(t, env, p.ID); len(rows) != 0 {
		t.Errorf("stored %+v", rows)
	}
	if hours, _ := env.db.DirtyHours(context.Background()); len(hours) != 0 {
		t.Errorf("dirty hours = %v, want none", hours)
	}
}
