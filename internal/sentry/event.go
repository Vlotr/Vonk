package sentry

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Event is one Sentry error event: the fields this tool reads, typed, plus the
// whole event as the SDK sent it (Raw), which is what gets stored.
//
// The typed fields are read leniently from Raw: SDKs send several shapes for
// the same field (a message as a string or an object, headers as an object or
// a list of pairs) and new SDKs add fields. A field with an unexpected type is
// left empty instead of failing the event.
type Event struct {
	EventID     string    // 32 lowercase hex characters, or "" when the SDK sent none
	Timestamp   time.Time // zero when missing or unreadable
	Platform    string
	Level       string // fatal, error, warning, info or debug; "error" when the SDK sent none
	Release     string
	Environment string
	ServerName  string
	// Message is the log message (logentry or message), formatted when the SDK formatted it.
	Message string
	// Exceptions are in Sentry's order: the innermost cause first, the
	// exception that was finally thrown last.
	Exceptions  []Exception
	Request     Request
	User        User
	Contexts    map[string]map[string]any
	Tags        map[string]string
	SDK         SDK
	Fingerprint []string // as set by the SDK; empty means the default grouping
	DebugImages []DebugImage

	// Raw is the whole event. Numbers are json.Number, so they are stored
	// exactly as sent. Call Refresh after changing it.
	Raw map[string]any

	// Size is how many bytes of JSON the event was decoded from. The ingest
	// queue adds it up to bound the memory that waiting events take.
	Size int
}

// Exception is one exception of an event.
type Exception struct {
	Type      string
	Value     string
	Module    string
	Mechanism Mechanism
	// Frames are in Sentry's order: the oldest call first, the frame that
	// raised the exception last.
	Frames []Frame
}

// Mechanism tells how the SDK caught the exception.
type Mechanism struct {
	Type    string
	Handled *bool // nil when the SDK did not say
}

// Frame is one line of a stack trace.
type Frame struct {
	Filename    string
	AbsPath     string
	Module      string
	Function    string
	Lineno      int
	Colno       int
	InApp       bool
	PreContext  []string
	ContextLine string
	PostContext []string
	Vars        map[string]any
}

// Request is the HTTP request during which the error happened.
type Request struct {
	URL         string
	Method      string
	QueryString string            // always in "a=1&b=2" form
	Headers     map[string]string // repeated headers are joined with ", "
	Env         map[string]string
	Data        any // the request body as the SDK sent it
}

// User is who saw the error.
type User struct {
	ID        string
	Email     string
	Username  string
	IPAddress string
}

// SDK names the client that sent the event.
type SDK struct {
	Name         string
	Version      string
	Packages     []Package
	Integrations []string
}

// Package is one package the SDK reports, such as "composer:sentry/sentry-laravel".
type Package struct {
	Name    string
	Version string
}

// DebugImage is an entry of debug_meta.images; browser SDKs list source maps here.
type DebugImage struct {
	Type     string
	DebugID  string
	CodeFile string
}

// DecodeEvent reads one event from JSON. It fails only when data is not a
// single JSON object.
func DecodeEvent(data []byte) (*Event, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var raw map[string]any
	if err := dec.Decode(&raw); err != nil {
		return nil, fmt.Errorf("event is not a JSON object: %w", err)
	}
	if raw == nil {
		return nil, errors.New("event is null")
	}
	if dec.More() {
		return nil, errors.New("event has data after the JSON object")
	}
	ev := &Event{Raw: raw, Size: len(data)}
	ev.Refresh()
	return ev, nil
}

// JSON encodes the whole event (Raw), without escaping <, > and &.
func (e *Event) JSON() ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(e.Raw); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// SetEventID sets the event id in the typed field and in Raw.
func (e *Event) SetEventID(id string) {
	e.EventID = id
	e.Raw["event_id"] = id
}

// Refresh reads the typed fields from Raw again, after Raw was changed.
func (e *Event) Refresh() {
	raw, size := e.Raw, e.Size
	*e = Event{
		Raw:         raw,
		Size:        size,
		EventID:     NormalizeEventID(str(raw["event_id"])),
		Timestamp:   timestamp(raw["timestamp"]),
		Platform:    str(raw["platform"]),
		Level:       level(raw["level"]),
		Release:     str(raw["release"]),
		Environment: str(raw["environment"]),
		ServerName:  str(raw["server_name"]),
		Message:     message(raw),
		Exceptions:  exceptions(raw["exception"]),
		Request:     request(obj(raw["request"])),
		User:        user(obj(raw["user"])),
		Contexts:    contexts(obj(raw["contexts"])),
		Tags:        stringPairs(raw["tags"]),
		SDK:         sdk(obj(raw["sdk"])),
		Fingerprint: strs(raw["fingerprint"]),
		DebugImages: debugImages(obj(raw["debug_meta"])),
	}
}

// level returns one of Sentry's levels (fatal, error, warning, info, debug);
// anything else, or nothing, is "error".
func level(v any) string {
	switch l := strings.ToLower(str(v)); l {
	case "fatal", "error", "warning", "info", "debug":
		return l
	case "warn":
		return "warning"
	case "critical":
		return "fatal"
	}
	return "error"
}

// NormalizeEventID returns id as 32 lowercase hex characters (dashes
// removed), or "" when it is not a valid event id.
func NormalizeEventID(id string) string {
	id = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(id), "-", ""))
	if len(id) != 32 {
		return ""
	}
	for _, c := range id {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return ""
		}
	}
	return id
}

func message(raw map[string]any) string {
	for _, key := range []string{"logentry", "message"} {
		switch v := raw[key].(type) {
		case string:
			if v != "" {
				return v
			}
		case map[string]any:
			if s := str(v["formatted"]); s != "" {
				return s
			}
			if s := str(v["message"]); s != "" {
				return s
			}
		}
	}
	return ""
}

// exceptions reads {"values": [...]}, a bare list, or a single exception object.
func exceptions(v any) []Exception {
	var values []any
	switch v := v.(type) {
	case []any:
		values = v
	case map[string]any:
		if list, ok := v["values"].([]any); ok {
			values = list
		} else if v["type"] != nil || v["value"] != nil {
			values = []any{v}
		}
	}

	var out []Exception
	for _, value := range values {
		m, ok := value.(map[string]any)
		if !ok {
			continue
		}
		mech := obj(m["mechanism"])
		exc := Exception{
			Type:      str(m["type"]),
			Value:     str(m["value"]),
			Module:    str(m["module"]),
			Mechanism: Mechanism{Type: str(mech["type"])},
			Frames:    frames(obj(m["stacktrace"])),
		}
		if handled, ok := mech["handled"].(bool); ok {
			exc.Mechanism.Handled = &handled
		}
		out = append(out, exc)
	}
	return out
}

func frames(stacktrace map[string]any) []Frame {
	list, _ := stacktrace["frames"].([]any)
	var out []Frame
	for _, value := range list {
		m, ok := value.(map[string]any)
		if !ok {
			continue
		}
		inApp, _ := m["in_app"].(bool)
		out = append(out, Frame{
			Filename:    str(m["filename"]),
			AbsPath:     str(m["abs_path"]),
			Module:      str(m["module"]),
			Function:    str(m["function"]),
			Lineno:      integer(m["lineno"]),
			Colno:       integer(m["colno"]),
			InApp:       inApp,
			PreContext:  strs(m["pre_context"]),
			ContextLine: str(m["context_line"]),
			PostContext: strs(m["post_context"]),
			Vars:        obj(m["vars"]),
		})
	}
	return out
}

func request(m map[string]any) Request {
	return Request{
		URL:         str(m["url"]),
		Method:      str(m["method"]),
		QueryString: queryString(m["query_string"]),
		Headers:     stringPairs(m["headers"]),
		Env:         stringPairs(m["env"]),
		Data:        m["data"],
	}
}

// queryString accepts "a=1&b=2", {"a": "1"} or [["a", "1"]] and returns "a=1&b=2".
func queryString(v any) string {
	if s, ok := v.(string); ok {
		return strings.TrimPrefix(s, "?")
	}
	pairs := stringPairs(v)
	if len(pairs) == 0 {
		return ""
	}
	values := url.Values{}
	for k, val := range pairs {
		values.Set(k, val)
	}
	return values.Encode()
}

func user(m map[string]any) User {
	return User{
		ID:        str(m["id"]),
		Email:     str(m["email"]),
		Username:  str(m["username"]),
		IPAddress: str(m["ip_address"]),
	}
}

func contexts(m map[string]any) map[string]map[string]any {
	out := map[string]map[string]any{}
	for name, value := range m {
		if ctx, ok := value.(map[string]any); ok {
			out[name] = ctx
		}
	}
	return out
}

func sdk(m map[string]any) SDK {
	s := SDK{Name: str(m["name"]), Version: str(m["version"]), Integrations: strs(m["integrations"])}
	packages, _ := m["packages"].([]any)
	for _, value := range packages {
		p := obj(value)
		if name := str(p["name"]); name != "" {
			s.Packages = append(s.Packages, Package{Name: name, Version: str(p["version"])})
		}
	}
	return s
}

func debugImages(debugMeta map[string]any) []DebugImage {
	images, _ := debugMeta["images"].([]any)
	var out []DebugImage
	for _, value := range images {
		m, ok := value.(map[string]any)
		if !ok {
			continue
		}
		out = append(out, DebugImage{Type: str(m["type"]), DebugID: str(m["debug_id"]), CodeFile: str(m["code_file"])})
	}
	return out
}

// timestamp reads unix seconds (a number or a numeric string) or an RFC 3339
// string; a string without a time zone is UTC. It returns zero when unreadable.
func timestamp(v any) time.Time {
	switch v := v.(type) {
	case json.Number:
		return unixSeconds(v.String())
	case string:
		for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999"} {
			if t, err := time.Parse(layout, v); err == nil {
				return t.UTC()
			}
		}
		return unixSeconds(v)
	}
	return time.Time{}
}

func unixSeconds(s string) time.Time {
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) || f <= 0 || f > 1e11 {
		return time.Time{}
	}
	sec, frac := math.Modf(f)
	return time.Unix(int64(sec), int64(math.Round(frac*1e6))*1000).UTC()
}

// stringPairs reads {"k": "v"}, {"k": ["v1", "v2"]} or [["k", "v"]] into a
// map of strings. Repeated values are joined with ", ".
func stringPairs(v any) map[string]string {
	out := map[string]string{}
	add := func(key string, value any) {
		if key == "" {
			return
		}
		s := str(value)
		if list, ok := value.([]any); ok {
			s = strings.Join(strs(list), ", ")
		}
		if prev, ok := out[key]; ok && prev != "" {
			s = prev + ", " + s
		}
		out[key] = s
	}
	switch v := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			add(k, v[k])
		}
	case []any:
		for _, pair := range v {
			if p, ok := pair.([]any); ok && len(p) == 2 {
				add(str(p[0]), p[1])
			}
		}
	}
	return out
}

// obj returns v as a JSON object, or nil.
func obj(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

// str returns strings as they are and numbers and booleans as text; anything else is "".
func str(v any) string {
	switch v := v.(type) {
	case string:
		return v
	case json.Number:
		return v.String()
	case bool:
		return strconv.FormatBool(v)
	}
	return ""
}

// strs returns the scalar values of a list as strings.
func strs(v any) []string {
	list, _ := v.([]any)
	var out []string
	for _, item := range list {
		switch item.(type) {
		case string, json.Number, bool:
			out = append(out, str(item))
		}
	}
	return out
}

// integer reads a number or a numeric string; anything else is 0.
func integer(v any) int {
	f, err := strconv.ParseFloat(str(v), 64)
	if err != nil || math.IsNaN(f) || f > math.MaxInt32 || f < math.MinInt32 {
		return 0
	}
	return int(f)
}
