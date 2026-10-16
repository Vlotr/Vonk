// Package auth signs people in to the dashboard and decides which projects
// they see.
//
// The first account is made on the setup page and is an admin; after that
// people join with an invite link an admin makes. A session is a random
// token in an HttpOnly cookie; the database keeps only an HMAC of it (keyed
// with SECRET_KEY), as it does for invite and reset tokens. Every change
// through the dashboard API also needs the session's CSRF token in the
// X-CSRF-Token header.
//
// RequireUser guards the dashboard; handlers read the signed-in user with
// UserFrom and must check every project id against CanAccessProject (or
// list only AccessibleProjectIDs). A member who asks for another project
// gets the same 404 as for a project that does not exist.
package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"log/slog"
	"net/url"
	"sync"
	"time"

	"monitor/internal/mail"
	"monitor/internal/store"
)

// How long things last.
const (
	// sessionTTL is how long a session lasts after it was last used.
	sessionTTL = 30 * 24 * time.Hour
	// sessionRenewEvery is how often a used session's expiry is moved
	// forward, so a page polling every minute does not write every minute.
	sessionRenewEvery = time.Hour
	inviteTTL         = 7 * 24 * time.Hour
	resetTTL          = time.Hour
)

// Mailer sends mail; *mail.Sender is one.
type Mailer interface {
	Send(ctx context.Context, msg mail.Message) error
}

// Options configure a Service.
type Options struct {
	DB *store.DB
	// SecretKey keys the hashes of stored tokens.
	SecretKey string
	// BaseURL is the dashboard's address; links in mail start with it, and
	// cookies are Secure when it is https.
	BaseURL string
	// Mailer sends invites and reset links; nil when MAIL_URL is not set.
	Mailer Mailer
	Logger *slog.Logger
	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

// Service holds sessions, logins, invites and password resets.
type Service struct {
	db         *store.DB
	tokenKey   []byte
	baseURL    string
	host       string // BASE_URL's host, for mail
	secure     bool
	cookieName string
	mailer     Mailer
	log        *slog.Logger
	now        func() time.Time

	logins *throttle // sign-in tries (and current-password checks)
	resets *throttle // reset-link requests

	// verify checks a password against a hash: VerifyPassword; tests count the calls.
	verify func(ctx context.Context, hash, password string) (bool, error)

	mailWG sync.WaitGroup // mail sent in the background
}

// New returns a Service.
func New(o Options) *Service {
	now := o.Now
	if now == nil {
		now = time.Now
	}
	// A key of its own for token hashes, derived from SECRET_KEY, so other
	// uses of SECRET_KEY never share it.
	key := hmac.New(sha256.New, []byte(o.SecretKey))
	key.Write([]byte("monitor/auth/token-hash/v1"))

	s := &Service{
		db:       o.DB,
		tokenKey: key.Sum(nil),
		baseURL:  o.BaseURL,
		mailer:   o.Mailer,
		log:      o.Logger,
		now:      now,
		logins:   newThrottle(failuresPerEmail, failuresPerClient, throttleWindow),
		resets:   newThrottle(failuresPerEmail, failuresPerClient, throttleWindow),
		verify:   VerifyPassword,
	}
	if u, err := url.Parse(o.BaseURL); err == nil {
		s.host = u.Host
		s.secure = u.Scheme == "https"
	}
	// Over https the __Host- prefix makes browsers refuse the cookie unless
	// it is Secure, for the whole host and set by the host itself.
	s.cookieName = "monitor_session"
	if s.secure {
		s.cookieName = "__Host-monitor_session"
	}
	return s
}

// MailEnabled reports whether invites and reset links are mailed.
func (s *Service) MailEnabled() bool {
	return s.mailer != nil
}

// newToken returns 32 random bytes as URL-safe text (43 characters).
func newToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// hashToken is what the database stores for a token: an HMAC keyed with
// SECRET_KEY, so neither a copy of the database nor a write to it is enough
// to sign in.
func (s *Service) hashToken(token string) string {
	h := hmac.New(sha256.New, s.tokenKey)
	h.Write([]byte(token))
	return hex.EncodeToString(h.Sum(nil))
}
