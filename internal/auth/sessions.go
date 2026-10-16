package auth

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"monitor/internal/httpjson"
	"monitor/internal/store"
)

// signIn starts a session for u: a new token in the cookie, its hash and a
// fresh CSRF token in the database. It answers with the user, as GET
// /ui/api/me does.
func (s *Service) signIn(w http.ResponseWriter, r *http.Request, u store.User) {
	ctx := r.Context()
	token, err := newToken()
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	csrf, err := newToken()
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	now := s.now()
	session, err := s.db.CreateSession(ctx, u.ID, s.hashToken(token), csrf, now, now.Add(sessionTTL))
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	// A good moment to clear out sessions nobody uses any more.
	if _, err := s.db.DeleteExpiredSessions(ctx, now); err != nil {
		s.log.Warn("auth: delete expired sessions", "err", err)
	}
	s.setCookie(w, token, session.ExpiresAt)
	httpjson.Write(w, http.StatusOK, s.meJSON(User{ID: u.ID, Email: u.Email, Role: u.Role}, session))
}

// setCookie sets the session cookie: HttpOnly (scripts cannot read it),
// SameSite=Lax (other sites' requests do not carry it, except plain links),
// and Secure when BASE_URL is https.
func (s *Service) setCookie(w http.ResponseWriter, token string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     s.cookieName,
		Value:    token,
		Path:     "/",
		Expires:  expires,
		MaxAge:   int(expires.Sub(s.now()).Seconds()),
		HttpOnly: true,
		Secure:   s.secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// clearCookie tells the browser to drop the session cookie.
func (s *Service) clearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     s.cookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   s.secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// errNoSession means the request carries no session that is still valid.
var errNoSession = errors.New("no valid session")

// currentSession returns the request's session and user. It returns
// errNoSession for a missing, unknown or expired one, or a database error.
func (s *Service) currentSession(r *http.Request) (string, store.Session, store.User, error) {
	cookie, err := r.Cookie(s.cookieName)
	if err != nil || cookie.Value == "" {
		return "", store.Session{}, store.User{}, errNoSession
	}
	session, u, err := s.db.SessionByTokenHash(r.Context(), s.hashToken(cookie.Value))
	if errors.Is(err, store.ErrNotFound) {
		return "", store.Session{}, store.User{}, errNoSession
	}
	if err != nil {
		return "", store.Session{}, store.User{}, err
	}
	if !s.now().Before(session.ExpiresAt) {
		return "", store.Session{}, store.User{}, errNoSession
	}
	return cookie.Value, session, u, nil
}

// RequireUser lets through only requests with a valid session, and only
// changes (POST, PATCH, DELETE ...) that carry the session's CSRF token.
// Without a session, the dashboard API answers 401 and pages redirect to
// the login page (or to the setup page while there is no account yet).
// The handlers behind it read the user with UserFrom.
func (s *Service) RequireUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, session, u, err := s.currentSession(r)
		if errors.Is(err, errNoSession) {
			if _, sent := r.Cookie(s.cookieName); sent == nil {
				s.clearCookie(w) // expired or unknown: no use keeping it
			}
			s.refuse(w, r)
			return
		}
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		if !safeMethod(r.Method) && !validCSRF(r, session) {
			httpjson.Error(w, http.StatusForbidden, "missing or wrong X-CSRF-Token; reload the page")
			return
		}

		// Sliding expiry: a session lasts 30 days after it was last used.
		now := s.now()
		if session.ExpiresAt.Sub(now) < sessionTTL-sessionRenewEvery {
			session.ExpiresAt = now.Add(sessionTTL)
			if err := s.db.ExtendSession(r.Context(), session.ID, session.ExpiresAt); err != nil {
				s.internalError(w, r, err)
				return
			}
			s.setCookie(w, token, session.ExpiresAt)
		}

		user, err := s.loadUser(r.Context(), u)
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		next.ServeHTTP(w, r.WithContext(withSignedIn(r.Context(), user, session)))
	})
}

// refuse answers a request without a session.
func (s *Service) refuse(w http.ResponseWriter, r *http.Request) {
	if isAPI(r) {
		httpjson.Error(w, http.StatusUnauthorized, "Sign in first.")
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "sign in first", http.StatusUnauthorized)
		return
	}
	n, err := s.db.CountUsers(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	target := "/setup"
	if n > 0 {
		target = "/login"
		if next := r.URL.RequestURI(); next != "/" {
			target += "?next=" + url.QueryEscape(next)
		}
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

func isAPI(r *http.Request) bool {
	return strings.HasPrefix(r.URL.Path, "/ui/api/")
}

// internalError logs err and answers 500 without details.
func (s *Service) internalError(w http.ResponseWriter, r *http.Request, err error) {
	s.log.Error("auth", "path", r.URL.Path, "err", err)
	if isAPI(r) {
		httpjson.Error(w, http.StatusInternalServerError, "internal error")
		return
	}
	http.Error(w, "internal error", http.StatusInternalServerError)
}
