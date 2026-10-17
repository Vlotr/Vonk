// Package testutil holds fakes that tests in several packages share. It is
// imported only by tests, so none of it ends up in the binary.
package testutil

import (
	"bufio"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"net"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// SMTPMode is how a fake SMTP server offers TLS.
type SMTPMode int

const (
	// SMTPStartTLS offers STARTTLS on a plain connection, like port 587.
	SMTPStartTLS SMTPMode = iota
	// SMTPImplicitTLS speaks TLS from the first byte, like port 465.
	SMTPImplicitTLS
	// SMTPPlain offers no TLS at all.
	SMTPPlain
)

// SMTPMessage is one mail the fake server received.
type SMTPMessage struct {
	From string
	To   []string
	// Data is the message as sent after DATA: headers, a blank line, the body.
	Data string
	// Auth is the decoded AUTH PLAIN response ("\x00user\x00pass"), or "".
	Auth string
	// TLS reports whether the message came over an encrypted connection.
	TLS bool
}

// SMTPServer is a minimal SMTP server on 127.0.0.1 that records what it is sent.
type SMTPServer struct {
	// Addr is host:port to send to.
	Addr string
	// RootCAs trusts the server's certificate (valid for 127.0.0.1).
	RootCAs *x509.CertPool

	listener net.Listener
	tlsConf  *tls.Config
	mode     SMTPMode

	mu       sync.Mutex
	messages []SMTPMessage
	wg       sync.WaitGroup
}

// NewSMTPServer starts a fake SMTP server that stops when the test ends.
func NewSMTPServer(t *testing.T, mode SMTPMode) *SMTPServer {
	t.Helper()
	// Borrow httptest's certificate for 127.0.0.1 instead of making one.
	https := httptest.NewTLSServer(nil)
	cert := https.TLS.Certificates[0]
	roots := x509.NewCertPool()
	roots.AddCert(https.Certificate())
	https.Close()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &SMTPServer{
		Addr:     listener.Addr().String(),
		RootCAs:  roots,
		listener: listener,
		tlsConf:  &tls.Config{Certificates: []tls.Certificate{cert}},
		mode:     mode,
	}
	s.wg.Add(1)
	go s.accept()
	t.Cleanup(func() {
		listener.Close()
		s.wg.Wait()
	})
	return s
}

// Messages returns the mails received so far.
func (s *SMTPServer) Messages() []SMTPMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]SMTPMessage(nil), s.messages...)
}

func (s *SMTPServer) accept() {
	defer s.wg.Done()
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer conn.Close()
			s.serve(conn)
		}()
	}
}

// serve speaks just enough SMTP for net/smtp: EHLO, STARTTLS, AUTH PLAIN,
// MAIL, RCPT, DATA, RSET, NOOP and QUIT.
func (s *SMTPServer) serve(conn net.Conn) {
	encrypted := false
	if s.mode == SMTPImplicitTLS {
		conn = tls.Server(conn, s.tlsConf)
		encrypted = true
	}
	r, w := bufio.NewReader(conn), bufio.NewWriter(conn)
	reply := func(lines ...string) {
		for _, line := range lines {
			w.WriteString(line + "\r\n")
		}
		w.Flush()
	}

	reply("220 fake.smtp ESMTP")
	var msg SMTPMessage
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		verb, arg, _ := strings.Cut(line, " ")
		switch strings.ToUpper(verb) {
		case "EHLO", "HELO":
			lines := []string{"250-fake.smtp"}
			if s.mode == SMTPStartTLS && !encrypted {
				lines = append(lines, "250-STARTTLS")
			}
			reply(append(lines, "250 AUTH PLAIN")...)
		case "STARTTLS":
			reply("220 go ahead")
			conn = tls.Server(conn, s.tlsConf)
			r, w = bufio.NewReader(conn), bufio.NewWriter(conn)
			encrypted = true
		case "AUTH":
			_, response, _ := strings.Cut(arg, " ")
			decoded, _ := base64.StdEncoding.DecodeString(response)
			msg.Auth = string(decoded)
			reply("235 ok")
		case "MAIL":
			msg.From = strings.Trim(strings.TrimPrefix(arg, "FROM:"), "<>")
			reply("250 ok")
		case "RCPT":
			msg.To = append(msg.To, strings.Trim(strings.TrimPrefix(arg, "TO:"), "<>"))
			reply("250 ok")
		case "DATA":
			reply("354 send it")
			var data strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if l == ".\r\n" {
					break
				}
				data.WriteString(strings.TrimPrefix(l, "."))
			}
			msg.Data, msg.TLS = data.String(), encrypted
			s.mu.Lock()
			s.messages = append(s.messages, msg)
			s.mu.Unlock()
			msg = SMTPMessage{Auth: msg.Auth}
			reply("250 queued")
		case "RSET", "NOOP":
			reply("250 ok")
		case "QUIT":
			reply("221 bye")
			return
		default:
			reply("502 not implemented")
		}
	}
}
