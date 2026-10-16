// Package posthog reads what PostHog SDKs (posthog-js above all) send:
// capture bodies in their encodings (JSON, gzip, base64 forms), the events
// in them, and the answers that keep the SDK quiet (flags, remote config).
package posthog

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"monitor/internal/scrub"
)

// Event names the dashboard counts.
const (
	EventPageview  = "$pageview"
	EventPageleave = "$pageleave"
)

// Most bytes an id or URL read from an event may have; longer ones are cut.
const (
	maxIDBytes  = 200
	maxURLBytes = 2000
)

// Event is one captured event. The fields page views need are read from
// the properties when the body is parsed, before Scrub filters the
// properties (it would filter "$session_id", whose key mentions a session).
type Event struct {
	UUID       string
	Name       string // "$pageview", "$pageleave", "custom", ...
	DistinctID string // the user id: anonymous until the app calls identify
	// Token is the event's own project token (properties.token, or api_key
	// or token on the event); "" when only the batch names one.
	Token string

	// Timestamp is when the event happened by the client's clock; zero
	// when it sent none. SentAt is when it sent the request, Offset (when
	// HasOffset) how long before sending the event happened; Time uses
	// them to correct the client's clock.
	Timestamp time.Time
	SentAt    time.Time
	Offset    time.Duration
	HasOffset bool

	// Read from the properties as sent.
	URL            string // $current_url
	Pathname       string // $pathname
	SessionID      string // $session_id
	WindowID       string // $window_id: one browser tab
	PageviewID     string // $pageview_id: the page view this event belongs to (the uuid of its $pageview)
	PrevPageviewID string // $prev_pageview_id
	RawUserAgent   string // $raw_user_agent
	BrowserType    string // $browser_type: "bot" when posthog-js was told to send bots too

	// Properties are all properties, scrubbed once Scrub ran. Numbers are json.Number.
	Properties map[string]any

	// Size is how many bytes of JSON the event was decoded from.
	Size int
}

// Time returns when the event happened, by the server's clock: PostHog's
// rule. With an offset, the receive time minus it; with a timestamp and
// sent_at, the receive time moved by their difference (so a client clock
// that is off cancels out); with a timestamp alone, the timestamp; else
// the receive time.
func (e Event) Time(received time.Time) time.Time {
	switch {
	case e.HasOffset:
		return received.Add(-e.Offset)
	case !e.Timestamp.IsZero() && !e.SentAt.IsZero():
		return received.Add(e.Timestamp.Sub(e.SentAt))
	case !e.Timestamp.IsZero():
		return e.Timestamp
	}
	return received
}

// Scrub filters the properties for storage: IP addresses are dropped and
// the key and URL rules of internal/scrub apply (passwords, tokens,
// cookies, sensitive query parameters).
func (e *Event) Scrub() {
	scrub.Properties(e.Properties)
}

// IsCustom reports whether an event name is the app's own (such as
// checkout_completed): PostHog's own events start with "$" ($pageview,
// $identify, $autocapture, ...).
func IsCustom(name string) bool {
	return !strings.HasPrefix(name, "$")
}

// sdkProperties are properties posthog-js adds without a "$": the project
// token and the user id, which the event already carries.
var sdkProperties = map[string]bool{"token": true, "distinct_id": true}

// OwnProperties returns the properties the app sent with a custom event:
// not the SDK's own ("$browser", "$current_url", ... and the token and
// user id), and not the ones without a value (posthog-js sends every
// campaign parameter, mostly null). Call Scrub first.
func (e *Event) OwnProperties() map[string]any {
	own := map[string]any{}
	for key, value := range e.Properties {
		if strings.HasPrefix(key, "$") || sdkProperties[key] || value == nil {
			continue
		}
		own[key] = value
	}
	return own
}

// eventFrom reads one event object. It returns false for an object
// without an event name.
func eventFrom(raw map[string]any, size int) (Event, bool) {
	props, _ := raw["properties"].(map[string]any)
	if props == nil {
		props = map[string]any{}
	}
	e := Event{
		UUID:           cut(str(raw["uuid"]), maxIDBytes),
		Name:           cut(str(raw["event"]), maxIDBytes),
		DistinctID:     cut(firstOf(raw["distinct_id"], props["distinct_id"], props["$distinct_id"]), maxIDBytes),
		Token:          firstOf(props["token"], raw["api_key"], raw["token"]),
		Timestamp:      timestamp(raw["timestamp"]),
		SentAt:         timestamp(raw["sent_at"]),
		URL:            cut(str(props["$current_url"]), maxURLBytes),
		Pathname:       cut(str(props["$pathname"]), maxURLBytes),
		SessionID:      cut(str(props["$session_id"]), maxIDBytes),
		WindowID:       cut(str(props["$window_id"]), maxIDBytes),
		PageviewID:     cut(str(props["$pageview_id"]), maxIDBytes),
		PrevPageviewID: cut(str(props["$prev_pageview_id"]), maxIDBytes),
		RawUserAgent:   cut(str(props["$raw_user_agent"]), maxURLBytes),
		BrowserType:    str(props["$browser_type"]),
		Properties:     props,
		Size:           size,
	}
	if ms, ok := number(raw["offset"]); ok {
		e.Offset, e.HasOffset = time.Duration(ms*float64(time.Millisecond)), true
	}
	return e, strings.TrimSpace(e.Name) != ""
}

// str returns v when it is a string, the digits when it is a number, else "".
func str(v any) string {
	switch v := v.(type) {
	case string:
		return v
	case json.Number:
		return v.String()
	}
	return ""
}

func firstOf(values ...any) string {
	for _, v := range values {
		if s := str(v); s != "" {
			return s
		}
	}
	return ""
}

func number(v any) (float64, bool) {
	n, ok := v.(json.Number)
	if !ok {
		return 0, false
	}
	f, err := n.Float64()
	return f, err == nil
}

// timestamp reads an ISO 8601 time, or unix milliseconds; zero when it is neither.
func timestamp(v any) time.Time {
	switch v := v.(type) {
	case string:
		for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999", "2006-01-02 15:04:05.999999999Z07:00"} {
			if t, err := time.Parse(layout, v); err == nil {
				return t.UTC()
			}
		}
	case json.Number:
		if ms, err := strconv.ParseInt(v.String(), 10, 64); err == nil && ms > 0 {
			return time.UnixMilli(ms).UTC()
		}
	}
	return time.Time{}
}

// cut shortens s to at most n bytes, at a character boundary.
func cut(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8Start(s[n]) {
		n--
	}
	return s[:n]
}

func utf8Start(b byte) bool { return b&0xc0 != 0x80 }
