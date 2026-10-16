// Package mail sends plain-text mail over SMTP, configured by MAIL_URL.
//
//	smtp://user:pass@host:587    STARTTLS (port 587 when left out)
//	smtps://user:pass@host:465   TLS from the start (port 465 when left out)
//	...?from=alerts@example.com  the sender; default monitor@<BASE_URL host>
//
// User name and password are optional and percent-encoded like any URL.
// Over smtp:// the server must offer STARTTLS, so a password or a link in a
// mail never crosses the network in clear text; only a relay on this
// machine (localhost) may do without.
package mail

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"mime/quotedprintable"
	"net"
	netmail "net/mail"
	"net/smtp"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Default ports.
const (
	defaultSMTPPort  = 587
	defaultSMTPSPort = 465
)

// sendTimeout limits one Send when the context has no deadline.
const sendTimeout = 30 * time.Second

// Config is a parsed MAIL_URL.
type Config struct {
	Host     string
	Port     int
	Username string // empty: no AUTH
	Password string
	// From is the sender, such as "alerts@example.com" or
	// `"Alerts" <alerts@example.com>`; empty means the default.
	From string
	// ImplicitTLS is true for smtps:// (TLS from the first byte).
	ImplicitTLS bool
}

// Parse reads a MAIL_URL. Its errors never repeat the password.
func Parse(mailURL string) (Config, error) {
	raw := strings.TrimSpace(mailURL)
	if raw == "" {
		return Config{}, errors.New("MAIL_URL is empty")
	}
	u, err := url.Parse(raw)
	if err != nil {
		// The *url.Error repeats the raw value, password included, so it is not wrapped.
		return Config{}, errors.New("MAIL_URL is not a valid URL; percent-encode special characters in the user name and password")
	}

	var cfg Config
	switch strings.ToLower(u.Scheme) {
	case "smtp":
		cfg.Port = defaultSMTPPort
	case "smtps":
		cfg.Port, cfg.ImplicitTLS = defaultSMTPSPort, true
	default:
		return Config{}, errors.New("MAIL_URL must start with smtp:// (STARTTLS) or smtps:// (TLS)")
	}
	cfg.Host = strings.ToLower(u.Hostname())
	if cfg.Host == "" {
		return Config{}, errors.New("MAIL_URL has no host, as in smtp://user:pass@mail.example.com:587")
	}
	if p := u.Port(); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > 65535 {
			return Config{}, errors.New("MAIL_URL has a port that is not a number from 1 to 65535")
		}
		cfg.Port = n
	}
	if u.User != nil {
		cfg.Username = u.User.Username()
		cfg.Password, _ = u.User.Password()
	}
	if u.Path != "" && u.Path != "/" {
		return Config{}, errors.New("MAIL_URL must not have a path; only ?from= may follow the host")
	}
	if u.Fragment != "" {
		return Config{}, errors.New("MAIL_URL must not have a #fragment")
	}

	query, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return Config{}, errors.New("MAIL_URL has a query that cannot be read")
	}
	for key := range query {
		if key != "from" {
			return Config{}, fmt.Errorf("MAIL_URL has an unknown setting %q; only from= is supported", key)
		}
	}
	if from := query.Get("from"); from != "" {
		addr, err := netmail.ParseAddress(from)
		if err != nil {
			return Config{}, fmt.Errorf("MAIL_URL from=%q is not a mail address", from)
		}
		cfg.From = formatAddress(addr)
	}
	return cfg, nil
}

// formatAddress writes an address without angle brackets when it has no name.
func formatAddress(addr *netmail.Address) string {
	if addr.Name == "" {
		return addr.Address
	}
	return addr.String()
}

// Message is one plain-text mail to one person.
type Message struct {
	To      string
	Subject string
	Text    string
}

// Sender sends mail with one Config.
type Sender struct {
	cfg  Config
	from *netmail.Address
	// requireTLS refuses servers that do not offer STARTTLS; false only
	// for a relay on this machine.
	requireTLS bool
	// rootCAs replaces the system's certificates; tests set it.
	rootCAs *x509.CertPool
}

// NewSender returns a Sender for cfg. Without a From in cfg, mail comes
// from monitor@<host of baseURL>.
func NewSender(cfg Config, baseURL string) *Sender {
	from := &netmail.Address{Address: "monitor@" + defaultFromHost(baseURL)}
	if cfg.From != "" {
		if addr, err := netmail.ParseAddress(cfg.From); err == nil {
			from = addr
		}
	}
	return &Sender{cfg: cfg, from: from, requireTLS: !isLocalhost(cfg.Host)}
}

func defaultFromHost(baseURL string) string {
	if u, err := url.Parse(baseURL); err == nil && u.Hostname() != "" {
		return u.Hostname()
	}
	return "localhost"
}

func isLocalhost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Send delivers msg. It gives up when ctx ends, or after 30 seconds when
// ctx has no deadline.
func (s *Sender) Send(ctx context.Context, msg Message) error {
	to, err := checkMessage(msg)
	if err != nil {
		return err
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, sendTimeout)
		defer cancel()
	}

	addr := net.JoinHostPort(s.cfg.Host, strconv.Itoa(s.cfg.Port))
	tlsConfig := &tls.Config{ServerName: s.cfg.Host, RootCAs: s.rootCAs, MinVersion: tls.VersionTLS12}
	dialer := &net.Dialer{}
	var conn net.Conn
	if s.cfg.ImplicitTLS {
		conn, err = (&tls.Dialer{NetDialer: dialer, Config: tlsConfig}).DialContext(ctx, "tcp", addr)
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("connect to the mail server %s: %w", addr, err)
	}
	// net/smtp knows no contexts: a deadline on the connection, and closing
	// it when ctx ends, stop a server that does not answer.
	if deadline, ok := ctx.Deadline(); ok {
		conn.SetDeadline(deadline)
	}
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()

	client, err := smtp.NewClient(conn, s.cfg.Host)
	if err != nil {
		conn.Close()
		return fmt.Errorf("mail server %s: %w", addr, err)
	}
	defer client.Close()

	if !s.cfg.ImplicitTLS {
		if ok, _ := client.Extension("STARTTLS"); ok {
			if err := client.StartTLS(tlsConfig); err != nil {
				return fmt.Errorf("mail server %s: STARTTLS: %w", addr, err)
			}
		} else if s.requireTLS {
			return fmt.Errorf("mail server %s does not offer STARTTLS; use a server that does, or smtps://", addr)
		}
	}
	if s.cfg.Username != "" {
		if err := client.Auth(smtp.PlainAuth("", s.cfg.Username, s.cfg.Password, s.cfg.Host)); err != nil {
			return fmt.Errorf("mail server %s: sign in: %w", addr, err)
		}
	}
	if err := client.Mail(s.from.Address); err != nil {
		return fmt.Errorf("mail server %s: MAIL FROM: %w", addr, err)
	}
	if err := client.Rcpt(to.Address); err != nil {
		return fmt.Errorf("mail server %s: RCPT TO: %w", addr, err)
	}
	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("mail server %s: DATA: %w", addr, err)
	}
	if err := s.write(w, to, msg); err != nil {
		return fmt.Errorf("mail server %s: write message: %w", addr, err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("mail server %s: message refused: %w", addr, err)
	}
	return client.Quit()
}

// checkMessage refuses line breaks in headers (they would let a value add
// headers of its own) and a recipient that is not one address.
func checkMessage(msg Message) (*netmail.Address, error) {
	if strings.ContainsAny(msg.To, "\r\n") || strings.ContainsAny(msg.Subject, "\r\n") {
		return nil, errors.New("mail: a header cannot contain a line break")
	}
	to, err := netmail.ParseAddress(msg.To)
	if err != nil {
		return nil, fmt.Errorf("mail: %q is not a mail address", msg.To)
	}
	return to, nil
}

// write writes the headers and the quoted-printable body.
func (s *Sender) write(w interface{ Write([]byte) (int, error) }, to *netmail.Address, msg Message) error {
	id := make([]byte, 16)
	rand.Read(id)
	headers := []string{
		"From: " + formatAddress(s.from),
		"To: " + formatAddress(to),
		"Subject: " + mime.QEncoding.Encode("utf-8", msg.Subject),
		"Date: " + time.Now().Format(time.RFC1123Z),
		"Message-ID: <" + hex.EncodeToString(id) + "@" + domainOf(s.from.Address) + ">",
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=utf-8",
		"Content-Transfer-Encoding: quoted-printable",
	}
	if _, err := w.Write([]byte(strings.Join(headers, "\r\n") + "\r\n\r\n")); err != nil {
		return err
	}
	body := quotedprintable.NewWriter(w)
	if _, err := body.Write([]byte(msg.Text)); err != nil {
		return err
	}
	return body.Close()
}

func domainOf(address string) string {
	if i := strings.LastIndex(address, "@"); i >= 0 {
		return address[i+1:]
	}
	return "localhost"
}
