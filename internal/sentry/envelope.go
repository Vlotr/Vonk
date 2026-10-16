// Package sentry reads what Sentry SDKs send: envelopes and the error events
// inside them. It also groups events (Fingerprint), names them (Summarize) and
// tells where they came from (Source).
//
// The envelope format is described at
// https://develop.sentry.dev/sdk/data-model/envelopes/:
//
//	{"event_id":"...","dsn":"...","sent_at":"..."}   envelope header, one JSON line
//	{"type":"event","length":41}                    item header, one JSON line
//	{...41 bytes of payload...}                     payload
//	{"type":"session"}                              item without length: the payload
//	{...}                                           runs to the next newline or EOF
package sentry

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// ErrBadEnvelope is wrapped by every error ParseEnvelope returns.
var ErrBadEnvelope = errors.New("bad envelope")

// maxHeaderBytes caps one envelope or item header line.
const maxHeaderBytes = 64 * 1024

// Envelope is a parsed envelope: its header and the error events it carried.
type Envelope struct {
	Header Header
	// Events are the "event" items, in order. Other item types (sessions,
	// client reports, transactions, attachments, ...) are skipped.
	Events []*Event
	// Skipped counts items that were not events.
	Skipped int
	// Dropped counts event items larger than the limit; they are not read.
	Dropped int
}

// Header is the envelope header.
type Header struct {
	EventID string // normalized like Event.EventID
	DSN     string // browser and PHP SDKs send their DSN here
	SentAt  string
}

// itemHeader is the JSON line before each item.
type itemHeader struct {
	Type   string `json:"type"`
	Length *int64 `json:"length"`
}

// ParseEnvelope reads an envelope from r. Event items larger than
// maxItemBytes are dropped (counted in Dropped). Every error wraps
// ErrBadEnvelope, and also the read error of r when there was one, so a
// caller can tell a body that was too large from one that was malformed.
func ParseEnvelope(r io.Reader, maxItemBytes int) (*Envelope, error) {
	br := bufio.NewReaderSize(r, 64*1024)
	env := &Envelope{}

	line, tooLong, err := readLine(br, maxHeaderBytes)
	if err == io.EOF {
		return nil, fmt.Errorf("%w: empty body", ErrBadEnvelope)
	}
	if err != nil {
		return nil, fmt.Errorf("%w: read: %w", ErrBadEnvelope, err)
	}
	if tooLong {
		return nil, fmt.Errorf("%w: envelope header longer than %d bytes", ErrBadEnvelope, maxHeaderBytes)
	}
	var header map[string]any
	if err := json.Unmarshal(line, &header); err != nil || header == nil {
		return nil, fmt.Errorf("%w: envelope header is not a JSON object", ErrBadEnvelope)
	}
	env.Header = Header{
		EventID: NormalizeEventID(str(header["event_id"])),
		DSN:     str(header["dsn"]),
		SentAt:  str(header["sent_at"]),
	}

	for n := 1; ; n++ {
		line, tooLong, err := readLine(br, maxHeaderBytes)
		if err == io.EOF {
			return env, nil
		}
		if err != nil {
			return nil, fmt.Errorf("%w: read: %w", ErrBadEnvelope, err)
		}
		if tooLong {
			return nil, fmt.Errorf("%w: item %d: header longer than %d bytes", ErrBadEnvelope, n, maxHeaderBytes)
		}
		if len(bytes.TrimSpace(line)) == 0 {
			continue // tolerate blank lines between items
		}

		var ih itemHeader
		if err := json.Unmarshal(line, &ih); err != nil {
			return nil, fmt.Errorf("%w: item %d: header is not valid JSON", ErrBadEnvelope, n)
		}
		if ih.Type == "" {
			return nil, fmt.Errorf("%w: item %d: header has no type", ErrBadEnvelope, n)
		}
		if ih.Length != nil && *ih.Length < 0 {
			return nil, fmt.Errorf("%w: item %d: negative length", ErrBadEnvelope, n)
		}

		isEvent := ih.Type == "event"
		payload, kept, err := readPayload(br, ih.Length, isEvent, maxItemBytes)
		if err != nil {
			return nil, fmt.Errorf("%w: item %d (%s): %w", ErrBadEnvelope, n, ih.Type, err)
		}
		switch {
		case !isEvent:
			env.Skipped++
		case !kept:
			env.Dropped++
		default:
			ev, err := DecodeEvent(payload)
			if err != nil {
				return nil, fmt.Errorf("%w: item %d: %v", ErrBadEnvelope, n, err)
			}
			if ev.EventID == "" && env.Header.EventID != "" {
				ev.SetEventID(env.Header.EventID)
			}
			env.Events = append(env.Events, ev)
		}
	}
}

// readPayload reads one item payload. With want false, or when the payload is
// larger than limit, it is discarded and kept is false.
func readPayload(br *bufio.Reader, length *int64, want bool, limit int) (payload []byte, kept bool, err error) {
	if length == nil {
		line, tooLong, err := readLine(br, limit)
		if err == io.EOF {
			return nil, want, nil // an empty payload at the very end
		}
		if err != nil {
			return nil, false, err
		}
		if !want || tooLong {
			return nil, false, nil
		}
		return line, true, nil
	}

	n := *length
	if want && n <= int64(limit) {
		payload = make([]byte, n)
		if _, err := io.ReadFull(br, payload); err != nil {
			return nil, false, truncated(err)
		}
		kept = true
	} else if _, err := io.CopyN(io.Discard, br, n); err != nil {
		return nil, false, truncated(err)
	}

	// After a payload with a length comes a newline or the end of the body.
	b, err := br.ReadByte()
	switch {
	case err == io.EOF:
		return payload, kept, nil
	case err != nil:
		return nil, false, err
	case b != '\n':
		return nil, false, errors.New("payload is longer than its length")
	}
	return payload, kept, nil
}

// truncated turns an early EOF into a clear message and keeps other read errors.
func truncated(err error) error {
	if err == io.EOF || err == io.ErrUnexpectedEOF {
		return errors.New("body ends before the payload does")
	}
	return err
}

// readLine reads up to the next "\n" (not included) or EOF and always
// consumes the whole line. When the line is longer than limit it keeps
// nothing and reports tooLong. It returns io.EOF only when nothing was left.
func readLine(br *bufio.Reader, limit int) (line []byte, tooLong bool, err error) {
	read := 0
	for {
		chunk, err := br.ReadSlice('\n')
		read += len(chunk)
		if err == nil {
			chunk = chunk[:len(chunk)-1] // drop the newline
		}
		if !tooLong {
			if len(line)+len(chunk) > limit {
				line, tooLong = nil, true
			} else {
				line = append(line, chunk...) // copies: ReadSlice's buffer is reused
			}
		}
		switch {
		case err == nil:
			return line, tooLong, nil
		case errors.Is(err, bufio.ErrBufferFull):
			// The line goes on; read the next part.
		case err == io.EOF:
			if read == 0 {
				return nil, false, io.EOF
			}
			return line, tooLong, nil
		default:
			return nil, false, err
		}
	}
}
