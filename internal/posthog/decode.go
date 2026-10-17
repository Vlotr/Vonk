package posthog

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/url"
	"strings"
	"time"
)

// Limits for one capture request.
const (
	DefaultMaxBytes      = 20 << 20 // the body after decompression
	DefaultMaxEventBytes = 1 << 20  // one event; larger events are dropped
)

// ErrTooLarge means the body is larger than Options.MaxBytes once decompressed.
var ErrTooLarge = errors.New("body larger than the limit")

// Options describe a capture request besides its body.
type Options struct {
	ContentType string
	// Compression is the compression query parameter: "gzip-js" (or
	// "gzip"), "base64", or "" to tell by the body itself.
	Compression string
	// Data is the data query parameter of a GET request; used when the
	// body is empty.
	Data string
	// MaxBytes limits the decompressed body; 0 means DefaultMaxBytes.
	MaxBytes int64
	// MaxEventBytes drops larger events; 0 means DefaultMaxEventBytes.
	MaxEventBytes int
}

// Batch is what one capture request carried.
type Batch struct {
	Token   string    // api_key or token at the top of the body; "" when only events name one
	SentAt  time.Time // sent_at at the top; zero when missing
	Events  []Event
	Dropped int // events larger than MaxEventBytes, or without an event name
}

// ParseBody reads a capture request's body (Content-Encoding already
// undone): a form with data= (plain, or base64), gzip (said by
// compression=gzip-js, or found by its first bytes, as posthog-js 1.438
// sends it), base64, or JSON. The JSON is a batch {"api_key", "batch": [...]},
// a list of events, or one event. Anything else is an error, and so is a
// body without events; ErrTooLarge when it grows past MaxBytes.
func ParseBody(body []byte, opts Options) (Batch, error) {
	if opts.MaxBytes <= 0 {
		opts.MaxBytes = DefaultMaxBytes
	}
	if opts.MaxEventBytes <= 0 {
		opts.MaxEventBytes = DefaultMaxEventBytes
	}
	payload, err := decodePayload(body, opts)
	if err != nil {
		return Batch{}, err
	}
	return parseJSON(payload, opts.MaxEventBytes)
}

// decodePayload undoes the form and the compression.
func decodePayload(body []byte, opts Options) ([]byte, error) {
	compression := strings.ToLower(strings.TrimSpace(opts.Compression))
	data := body
	mediaType, _, _ := mime.ParseMediaType(opts.ContentType)
	switch {
	case len(body) == 0 && opts.Data != "":
		data = []byte(opts.Data)
	case mediaType == "application/x-www-form-urlencoded" || bytes.HasPrefix(body, []byte("data=")):
		form, err := url.ParseQuery(string(body))
		if err != nil {
			return nil, fmt.Errorf("form body: %v", err)
		}
		if form.Get("data") == "" {
			return nil, errors.New("form body has no data field")
		}
		data = []byte(form.Get("data"))
		if compression == "" {
			compression = strings.ToLower(form.Get("compression"))
		}
	}

	switch compression {
	case "gzip-js", "gzip":
		return gunzip(data, opts.MaxBytes)
	case "base64":
		return unbase64(data)
	case "":
		switch {
		case len(data) >= 2 && data[0] == 0x1f && data[1] == 0x8b:
			return gunzip(data, opts.MaxBytes)
		case looksLikeJSON(data):
			return data, nil
		default:
			return unbase64(data)
		}
	}
	return nil, fmt.Errorf("compression %q is not supported; use gzip-js or base64", compression)
}

func looksLikeJSON(data []byte) bool {
	trimmed := bytes.TrimSpace(data)
	return len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '[')
}

func gunzip(data []byte, limit int64) ([]byte, error) {
	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("body is not valid gzip: %v", err)
	}
	out, err := io.ReadAll(io.LimitReader(zr, limit+1))
	if err != nil {
		return nil, fmt.Errorf("body is not valid gzip: %v", err)
	}
	if int64(len(out)) > limit {
		return nil, ErrTooLarge
	}
	return out, nil
}

// unbase64 decodes standard base64, with or without padding. A form may
// turn "+" into a space on the way, so spaces count as "+".
func unbase64(data []byte) ([]byte, error) {
	s := strings.ReplaceAll(strings.TrimSpace(string(data)), " ", "+")
	out, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		out, err = base64.RawStdEncoding.DecodeString(strings.TrimRight(s, "="))
	}
	if err != nil {
		return nil, errors.New("body is neither JSON, gzip nor base64")
	}
	return out, nil
}

// parseJSON reads the batch, list or single event. Each event is decoded
// on its own, so its size is known and one large event is dropped alone.
func parseJSON(payload []byte, maxEventBytes int) (Batch, error) {
	var batch Batch
	var items []json.RawMessage
	trimmed := bytes.TrimSpace(payload)
	switch {
	case len(trimmed) > 0 && trimmed[0] == '[':
		if err := json.Unmarshal(trimmed, &items); err != nil {
			return Batch{}, fmt.Errorf("body is not valid JSON: %v", err)
		}
	case len(trimmed) > 0 && trimmed[0] == '{':
		var top map[string]json.RawMessage
		if err := json.Unmarshal(trimmed, &top); err != nil {
			return Batch{}, fmt.Errorf("body is not valid JSON: %v", err)
		}
		batch.Token = firstOf(rawString(top["api_key"]), rawString(top["token"]))
		batch.SentAt = timestamp(rawString(top["sent_at"]))
		switch {
		case top["batch"] != nil:
			if err := json.Unmarshal(top["batch"], &items); err != nil {
				return Batch{}, fmt.Errorf("batch is not a list: %v", err)
			}
		case top["event"] != nil:
			items = []json.RawMessage{trimmed}
		}
	default:
		return Batch{}, errors.New("body is not JSON")
	}
	if len(items) == 0 {
		return Batch{}, errors.New("body has no events")
	}

	for _, item := range items {
		if len(item) > maxEventBytes {
			batch.Dropped++
			continue
		}
		dec := json.NewDecoder(bytes.NewReader(item))
		dec.UseNumber()
		var raw map[string]any
		if err := dec.Decode(&raw); err != nil || raw == nil {
			return Batch{}, errors.New("an event in the batch is not a JSON object")
		}
		ev, ok := eventFrom(raw, len(item))
		if !ok {
			batch.Dropped++
			continue
		}
		if ev.SentAt.IsZero() {
			ev.SentAt = batch.SentAt
		}
		batch.Events = append(batch.Events, ev)
	}
	return batch, nil
}

// rawString returns a JSON string's value, or a JSON number's digits, or "".
func rawString(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if dec.Decode(&v) != nil {
		return nil
	}
	return v
}
