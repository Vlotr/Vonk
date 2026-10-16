package api

import (
	"encoding/json"
	"strings"
	"time"

	"monitor/internal/sentry"
	"monitor/internal/store"
)

// contextLines is how many source lines the stack trace shows before and
// after the error line, as in the design. SDKs usually send five.
const contextLines = 3

// eventJSON is the newest event of an issue, as its page shows it. Optional
// fields are left out when the event does not have them.
type eventJSON struct {
	EventID   string         `json:"eventId"`
	Timestamp time.Time      `json:"timestamp"`
	Exception *exceptionJSON `json:"exception,omitempty"`
	// Frames are the main exception's stack trace, the most recent call
	// first: the frame that raised the exception is frames[0].
	Frames      []frameJSON  `json:"frames"`
	User        *userJSON    `json:"user,omitempty"`
	Browser     string       `json:"browser,omitempty"` // the visitor's browser, such as "Chrome 129"
	OS          string       `json:"os,omitempty"`      // the visitor's system, such as "macOS"
	Environment string       `json:"environment,omitempty"`
	Server      string       `json:"server,omitempty"`
	Runtime     *runtimeJSON `json:"runtime,omitempty"`
	URL         string       `json:"url,omitempty"`
	Release     string       `json:"release,omitempty"`
	// Country is an ISO 3166 code, only when the SDK sent one (IP addresses are never stored).
	Country string `json:"country,omitempty"`
}

type exceptionJSON struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

type frameJSON struct {
	File        string   `json:"file"`
	Function    string   `json:"function"`
	Line        int      `json:"line"`
	InApp       bool     `json:"inApp"`
	PreContext  []string `json:"preContext"`
	ContextLine string   `json:"contextLine"`
	PostContext []string `json:"postContext"`
}

type userJSON struct {
	ID string `json:"id"`
}

type runtimeJSON struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// eventDetail reads a stored event for the issue page. When the stored JSON
// cannot be read it still returns the id and time, with the error.
func eventDetail(stored store.ErrorEvent) (*eventJSON, error) {
	out := &eventJSON{EventID: stored.EventID, Timestamp: stored.Timestamp, Frames: []frameJSON{}}
	ev, err := sentry.DecodeEvent([]byte(stored.Data))
	if err != nil {
		return out, err
	}

	if exc := sentry.MainException(ev); exc != nil {
		out.Exception = &exceptionJSON{Type: exc.Type, Value: exc.Value}
		out.Frames = framesJSON(exc.Frames)
	}
	if ev.User.ID != "" {
		out.User = &userJSON{ID: ev.User.ID}
	}
	out.Browser, out.OS = client(ev)
	out.Environment = ev.Environment
	out.Server = ev.ServerName
	out.URL = ev.Request.URL
	out.Release = ev.Release
	if rt := ev.Contexts["runtime"]; rt != nil {
		name, version := text(rt["name"]), text(rt["version"])
		if name != "" || version != "" {
			out.Runtime = &runtimeJSON{Name: name, Version: version}
		}
	}
	out.Country = countryCode(ev)
	return out, nil
}

// framesJSON turns Sentry's frames (oldest call first) into the most recent
// call first, keeping contextLines of source on each side of the error line.
func framesJSON(frames []sentry.Frame) []frameJSON {
	out := make([]frameJSON, 0, len(frames))
	for i := len(frames) - 1; i >= 0; i-- {
		f := frames[i]
		file := f.Filename
		if file == "" {
			file = f.AbsPath
		}
		if file == "" {
			file = f.Module
		}
		pre, post := f.PreContext, f.PostContext
		if len(pre) > contextLines {
			pre = pre[len(pre)-contextLines:]
		}
		if len(post) > contextLines {
			post = post[:contextLines]
		}
		out = append(out, frameJSON{
			File:        file,
			Function:    f.Function,
			Line:        f.Lineno,
			InApp:       f.InApp,
			PreContext:  append([]string{}, pre...),
			ContextLine: f.ContextLine,
			PostContext: append([]string{}, post...),
		})
	}
	return out
}

// client names the visitor's browser and system. The User-Agent header of
// the request is preferred: server SDKs put the server's own system in
// contexts.os. Without it, Sentry's browser and client_os contexts are used.
func client(ev *sentry.Event) (browser, os string) {
	for name, value := range ev.Request.Headers {
		if strings.EqualFold(name, "User-Agent") {
			if browser, os = parseUserAgent(value); browser != "" || os != "" {
				return browser, os
			}
		}
	}
	return nameAndVersion(ev.Contexts["browser"]), nameAndVersion(ev.Contexts["client_os"])
}

// nameAndVersion returns "Chrome 129" for a context {"name": "Chrome", "version": "129.0.1"}.
func nameAndVersion(ctx map[string]any) string {
	name := text(ctx["name"])
	if name == "" {
		return ""
	}
	if v := majorVersion(text(ctx["version"])); v != "" {
		return name + " " + v
	}
	return name
}

// countryCode returns user.geo.country_code when it is two letters.
func countryCode(ev *sentry.Event) string {
	user, _ := ev.Raw["user"].(map[string]any)
	geo, _ := user["geo"].(map[string]any)
	code := strings.ToUpper(text(geo["country_code"]))
	if len(code) != 2 || code[0] < 'A' || code[0] > 'Z' || code[1] < 'A' || code[1] > 'Z' {
		return ""
	}
	return code
}

// text returns a JSON string or number as text, anything else as "".
func text(v any) string {
	switch v := v.(type) {
	case string:
		return v
	case json.Number:
		return v.String()
	}
	return ""
}
