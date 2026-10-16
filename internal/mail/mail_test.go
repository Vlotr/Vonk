package mail

import (
	"context"
	"io"
	"mime"
	"mime/quotedprintable"
	"net"
	netmail "net/mail"
	"strconv"
	"strings"
	"testing"
	"time"

	"monitor/internal/testutil"
)

func TestParse(t *testing.T) {
	tests := []struct {
		url  string
		want Config
	}{
		{"smtp://jan:secret@mail.example.com:587", Config{Host: "mail.example.com", Port: 587, Username: "jan", Password: "secret"}},
		{"smtp://jan:secret@mail.example.com", Config{Host: "mail.example.com", Port: 587, Username: "jan", Password: "secret"}},
		{"smtps://jan:secret@mail.example.com:465", Config{Host: "mail.example.com", Port: 465, Username: "jan", Password: "secret", ImplicitTLS: true}},
		{"smtps://mail.example.com", Config{Host: "mail.example.com", Port: 465, ImplicitTLS: true}},
		{"SMTP://Mail.Example.com:2525/", Config{Host: "mail.example.com", Port: 2525}},
		// Percent-encoded user names and passwords, as URLs need for @ : / and spaces.
		{"smtp://apikey%40example.com:p%40ss%3Aw%2Frd%20x@smtp.example.com:587", Config{
			Host: "smtp.example.com", Port: 587, Username: "apikey@example.com", Password: "p@ss:w/rd x"}},
		{"smtp://jan:secret@mail.example.com:587?from=alerts@example.com", Config{
			Host: "mail.example.com", Port: 587, Username: "jan", Password: "secret", From: "alerts@example.com"}},
		{"smtp://mail.example.com?from=Monitor%20%3Cmonitor%40example.com%3E", Config{
			Host: "mail.example.com", Port: 587, From: "\"Monitor\" <monitor@example.com>"}},
		{"smtp://[2001:db8::1]:25", Config{Host: "2001:db8::1", Port: 25}},
	}
	for _, tt := range tests {
		got, err := Parse(tt.url)
		if err != nil {
			t.Errorf("Parse(%q): %v", tt.url, err)
			continue
		}
		if got != tt.want {
			t.Errorf("Parse(%q) = %+v, want %+v", tt.url, got, tt.want)
		}
	}
}

func TestParse_Errors(t *testing.T) {
	for _, raw := range []string{
		"",
		"mail.example.com:587",
		"http://jan:secret@mail.example.com",
		"smtp://jan:secret@:587",
		"smtp://jan:secret@mail.example.com:port",
		"smtp://jan:secret@mail.example.com:0",
		"smtp://jan:secret@mail.example.com:70000",
		"smtp://jan:secret@mail.example.com/some/path",
		"smtp://jan:secret@mail.example.com?form=typo@example.com",
		"smtp://jan:secret@mail.example.com?from=not-an-address",
		"smtp://jan:secret@mail.example.com#frag",
		"smtp://jan:secret%zz@mail.example.com",
	} {
		_, err := Parse(raw)
		if err == nil {
			t.Errorf("Parse(%q): want an error", raw)
			continue
		}
		if strings.Contains(err.Error(), "secret") {
			t.Errorf("Parse(%q) error repeats the password: %v", raw, err)
		}
	}
}

// configFor points a Config at a fake server.
func configFor(t *testing.T, srv *testutil.SMTPServer, implicit bool) Config {
	t.Helper()
	host, port, _ := net.SplitHostPort(srv.Addr)
	n, _ := strconv.Atoi(port)
	return Config{Host: host, Port: n, Username: "jan", Password: "s3cret", ImplicitTLS: implicit}
}

// testSender trusts the fake server's certificate.
func testSender(t *testing.T, srv *testutil.SMTPServer, cfg Config) *Sender {
	t.Helper()
	s := NewSender(cfg, "https://monitor.example.com:8443")
	s.rootCAs = srv.RootCAs
	return s
}

// readBody decodes the quoted-printable body of a received message.
func readMessage(t *testing.T, data string) (*netmail.Message, string) {
	t.Helper()
	m, err := netmail.ReadMessage(strings.NewReader(data))
	if err != nil {
		t.Fatalf("read message: %v\n%s", err, data)
	}
	body, err := io.ReadAll(quotedprintable.NewReader(m.Body))
	if err != nil {
		t.Fatal(err)
	}
	return m, string(body)
}

func TestSend_STARTTLS(t *testing.T) {
	srv := testutil.NewSMTPServer(t, testutil.SMTPStartTLS)
	s := testSender(t, srv, configFor(t, srv, false))
	s.requireTLS = true // as for any host other than this machine

	long := "Open this link to choose a password: https://monitor.example.com/invite?token=" + strings.Repeat("A", 80)
	err := s.Send(context.Background(), Message{To: "piet@example.com", Subject: "Uitnodiging für Piet", Text: "Hello Piet,\n\n" + long + "\n"})
	if err != nil {
		t.Fatal(err)
	}
	got := srv.Messages()
	if len(got) != 1 {
		t.Fatalf("server got %d messages, want 1", len(got))
	}
	msg := got[0]
	if !msg.TLS {
		t.Error("message was not sent over TLS")
	}
	if msg.Auth != "\x00jan\x00s3cret" {
		t.Errorf("AUTH PLAIN = %q", msg.Auth)
	}
	if msg.From != "monitor@monitor.example.com" || len(msg.To) != 1 || msg.To[0] != "piet@example.com" {
		t.Errorf("envelope from %q to %v", msg.From, msg.To)
	}

	m, body := readMessage(t, msg.Data)
	if from := m.Header.Get("From"); from != "<monitor@monitor.example.com>" && from != "monitor@monitor.example.com" {
		t.Errorf("From header = %q, want the default monitor@<BASE_URL host>", from)
	}
	subject, err := new(mime.WordDecoder).DecodeHeader(m.Header.Get("Subject"))
	if err != nil || subject != "Uitnodiging für Piet" {
		t.Errorf("Subject = %q (%v)", subject, err)
	}
	for _, h := range []string{"Date", "Message-Id", "Mime-Version"} {
		if m.Header.Get(h) == "" {
			t.Errorf("header %s missing", h)
		}
	}
	if ct := m.Header.Get("Content-Type"); ct != "text/plain; charset=utf-8" {
		t.Errorf("Content-Type = %q", ct)
	}
	if !strings.Contains(body, long) {
		t.Errorf("body lost the long link line:\n%s", body)
	}
}

func TestSend_ImplicitTLSAndFrom(t *testing.T) {
	srv := testutil.NewSMTPServer(t, testutil.SMTPImplicitTLS)
	cfg := configFor(t, srv, true)
	cfg.From = `"Alerts" <alerts@example.com>`
	s := testSender(t, srv, cfg)
	s.requireTLS = true

	if err := s.Send(context.Background(), Message{To: "piet@example.com", Subject: "Hi", Text: "Body"}); err != nil {
		t.Fatal(err)
	}
	got := srv.Messages()
	if len(got) != 1 || !got[0].TLS || got[0].From != "alerts@example.com" {
		t.Fatalf("messages = %+v", got)
	}
	m, _ := readMessage(t, got[0].Data)
	if from := m.Header.Get("From"); from != `"Alerts" <alerts@example.com>` {
		t.Errorf("From header = %q", from)
	}
}

func TestSend_RefusesWithoutTLS(t *testing.T) {
	srv := testutil.NewSMTPServer(t, testutil.SMTPPlain)
	s := testSender(t, srv, configFor(t, srv, false))
	s.requireTLS = true

	err := s.Send(context.Background(), Message{To: "piet@example.com", Subject: "Hi", Text: "Body"})
	if err == nil || !strings.Contains(err.Error(), "STARTTLS") {
		t.Errorf("Send to a server without STARTTLS: err = %v, want a STARTTLS error", err)
	}
	if n := len(srv.Messages()); n != 0 {
		t.Errorf("server got %d messages, want none", n)
	}
}

func TestSend_PlainOnlyToThisMachine(t *testing.T) {
	srv := testutil.NewSMTPServer(t, testutil.SMTPPlain)
	cfg := configFor(t, srv, false)
	cfg.Username, cfg.Password = "", ""
	s := testSender(t, srv, cfg)
	if s.requireTLS {
		t.Fatal("a relay on 127.0.0.1 should not need TLS")
	}
	if err := s.Send(context.Background(), Message{To: "piet@example.com", Subject: "Hi", Text: "Body"}); err != nil {
		t.Fatal(err)
	}
	if got := srv.Messages(); len(got) != 1 || got[0].TLS {
		t.Errorf("messages = %+v, want one without TLS", got)
	}
}

func TestSend_RefusesHeaderInjection(t *testing.T) {
	s := NewSender(Config{Host: "127.0.0.1", Port: 1}, "https://monitor.example.com")
	for _, msg := range []Message{
		{To: "piet@example.com\r\nBcc: all@example.com", Subject: "Hi", Text: "x"},
		{To: "piet@example.com", Subject: "Hi\r\nBcc: all@example.com", Text: "x"},
		{To: "not an address", Subject: "Hi", Text: "x"},
		{To: "", Subject: "Hi", Text: "x"},
	} {
		if err := s.Send(context.Background(), msg); err == nil {
			t.Errorf("Send(%+v): want an error", msg)
		}
	}
}

func TestSend_GivesUpWithTheContext(t *testing.T) {
	// A server that accepts and then says nothing.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			defer conn.Close()
			time.Sleep(5 * time.Second)
		}
	}()
	host, port, _ := net.SplitHostPort(listener.Addr().String())
	n, _ := strconv.Atoi(port)
	s := NewSender(Config{Host: host, Port: n}, "https://monitor.example.com")

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := s.Send(ctx, Message{To: "piet@example.com", Subject: "Hi", Text: "x"}); err == nil {
		t.Error("Send to a silent server: want an error")
	}
	if waited := time.Since(start); waited > 2*time.Second {
		t.Errorf("Send waited %v, want about the context's 200ms", waited)
	}
}
