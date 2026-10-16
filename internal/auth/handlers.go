package auth

import (
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"time"

	"monitor/internal/httpjson"
	"monitor/internal/store"
)

// maxAuthBody caps the JSON bodies of the sign-in endpoints.
const maxAuthBody = 8 << 10

// PublicPages are the dashboard pages anyone may open, because they are
// how people sign in. Every other page needs a session.
var PublicPages = []string{"/setup", "/login", "/invite", "/forgot-password", "/reset-password"}

// RegisterPublic adds the endpoints that work without a session:
//
//	GET  /ui/api/auth/status     {setupNeeded, mailEnabled}
//	POST /ui/api/setup           {email, password}: the first account, an admin; 404 once one exists
//	POST /ui/api/login           {email, password}
//	POST /ui/api/invite/check         {token}: {email, role} of an invite
//	POST /ui/api/invite/accept        {token, password}
//	POST /ui/api/password/forgot      {email}: mails a reset link when the account exists
//	POST /ui/api/password/reset/check {token}: {email} of a reset link
//	POST /ui/api/password/reset       {token, password}
//
// Each one that signs someone in answers like GET /ui/api/me and sets the
// session cookie. Links carry their token after "#", which browsers never
// send, and the pages post it in a body: tokens appear in no URL a server
// or proxy logs.
func (s *Service) RegisterPublic(mux *http.ServeMux) {
	mux.HandleFunc("GET /ui/api/auth/status", s.authStatus)
	mux.HandleFunc("POST /ui/api/setup", s.setup)
	mux.HandleFunc("POST /ui/api/login", s.login)
	mux.HandleFunc("POST /ui/api/invite/check", s.inviteDetails)
	mux.HandleFunc("POST /ui/api/invite/accept", s.acceptInvite)
	mux.HandleFunc("POST /ui/api/password/forgot", s.forgotPassword)
	mux.HandleFunc("POST /ui/api/password/reset/check", s.resetDetails)
	mux.HandleFunc("POST /ui/api/password/reset", s.resetPassword)
}

// Register adds the endpoints for the signed-in user; mount them behind RequireUser:
//
//	GET  /ui/api/me           {id, email, role, csrfToken, mailEnabled}
//	POST /ui/api/logout
//	POST /ui/api/me/password  {currentPassword, newPassword}
func (s *Service) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /ui/api/me", s.me)
	mux.HandleFunc("POST /ui/api/logout", s.logout)
	mux.HandleFunc("POST /ui/api/me/password", s.changePassword)
}

// meJSON is the signed-in user as the dashboard reads it.
type meJSON struct {
	ID    int64  `json:"id"`
	Email string `json:"email"`
	Role  string `json:"role"`
	// CSRFToken goes in the X-CSRF-Token header of every change.
	CSRFToken   string `json:"csrfToken"`
	MailEnabled bool   `json:"mailEnabled"`
}

func (s *Service) meJSON(u User, session store.Session) meJSON {
	return meJSON{ID: u.ID, Email: u.Email, Role: u.Role, CSRFToken: session.CSRFToken, MailEnabled: s.MailEnabled()}
}

// credentials is the body of setup and login.
type credentials struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func (s *Service) authStatus(w http.ResponseWriter, r *http.Request) {
	n, err := s.db.CountUsers(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusOK, map[string]bool{"setupNeeded": n == 0, "mailEnabled": s.MailEnabled()})
}

// setup creates the first account. Once any account exists it answers 404:
// registration is closed and people join by invite.
func (s *Service) setup(w http.ResponseWriter, r *http.Request) {
	var req credentials
	if !httpjson.Read(w, r, maxAuthBody, &req) {
		return
	}
	ctx := r.Context()
	if n, err := s.db.CountUsers(ctx); err != nil {
		s.internalError(w, r, err)
		return
	} else if n > 0 {
		httpjson.Error(w, http.StatusNotFound, "not found")
		return
	}
	email := store.NormalizeEmail(req.Email)
	if err := firstError(CheckEmail(email), CheckPassword(req.Password)); err != nil {
		httpjson.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	hash, err := HashPassword(ctx, req.Password)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	u, err := s.db.CreateFirstAdmin(ctx, email, hash, s.now())
	if errors.Is(err, store.ErrUsersExist) {
		httpjson.Error(w, http.StatusNotFound, "not found") // someone else was faster
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	s.log.Info("auth: first account created", "user", u.ID)
	s.signIn(w, r, u)
}

// login checks an address and password. Every try is reserved in the
// throttle first and stays counted when it fails; an unknown address is
// checked against a dummy hash, so it answers the same, as slowly.
func (s *Service) login(w http.ResponseWriter, r *http.Request) {
	var req credentials
	if !httpjson.Read(w, r, maxAuthBody, &req) {
		return
	}
	ctx := r.Context()
	email := store.NormalizeEmail(req.Email)
	// An address that cannot have an account is refused before it counts:
	// the throttle only ever tracks real-looking addresses.
	if err := CheckEmail(email); err != nil {
		httpjson.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	// Whether the address has an account decides which throttle store
	// counts it (accounts are never evicted); both answer alike.
	u, err := s.db.UserByEmail(ctx, email)
	found := err == nil
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		s.internalError(w, r, err)
		return
	}
	// The try is reserved before the slow password check, so requests
	// arriving at the same moment cannot all get through.
	reserved, wait, allowed := s.logins.begin(email, found, s.logins.clientKey(r), s.now())
	if !allowed {
		tooManyAttempts(w, wait)
		return
	}
	hash := dummyHash()
	if found {
		hash = u.PasswordHash
	}
	ok, err := VerifyPassword(ctx, hash, req.Password)
	if err != nil {
		if ctx.Err() != nil {
			return // the client went away while waiting for a hash slot
		}
		s.log.Error("auth: stored password hash cannot be read", "user", u.ID, "err", err)
	}
	if !found || !ok {
		// The reserved try stays: it was a failure.
		httpjson.Error(w, http.StatusUnauthorized, "The email or password is wrong.")
		return
	}
	s.logins.succeeded(reserved)
	s.signIn(w, r, u)
}

// tooManyAttempts answers 429 with how long to wait.
func tooManyAttempts(w http.ResponseWriter, wait time.Duration) {
	w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(wait.Seconds()))))
	minutes := int(math.Ceil(wait.Minutes()))
	unit := "minutes"
	if minutes == 1 {
		unit = "minute"
	}
	httpjson.Error(w, http.StatusTooManyRequests, fmt.Sprintf("Too many attempts. Try again in %d %s.", minutes, unit))
}

func (s *Service) me(w http.ResponseWriter, r *http.Request) {
	session, _ := sessionFrom(r.Context())
	httpjson.Write(w, http.StatusOK, s.meJSON(UserFrom(r.Context()), session))
}

func (s *Service) logout(w http.ResponseWriter, r *http.Request) {
	session, _ := sessionFrom(r.Context())
	if err := s.db.DeleteSession(r.Context(), session.ID); err != nil {
		s.internalError(w, r, err)
		return
	}
	s.clearCookie(w)
	httpjson.Write(w, http.StatusOK, struct{}{})
}

// changePassword sets a new password for the signed-in user after checking
// the current one, and signs out their other sessions. A wrong current
// password counts toward the sign-in throttle, so a stolen session cannot
// be used to guess it; a right one gives the try back and starts the
// address's count over, as a sign-in does.
func (s *Service) changePassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CurrentPassword string `json:"currentPassword"`
		NewPassword     string `json:"newPassword"`
	}
	if !httpjson.Read(w, r, maxAuthBody, &req) {
		return
	}
	ctx := r.Context()
	user := UserFrom(ctx)
	session, _ := sessionFrom(ctx)
	reserved, wait, allowed := s.logins.begin(user.Email, true, s.logins.clientKey(r), s.now())
	if !allowed {
		tooManyAttempts(w, wait)
		return
	}
	stored, err := s.db.UserByID(ctx, user.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if ok, _ := VerifyPassword(ctx, stored.PasswordHash, req.CurrentPassword); !ok {
		httpjson.Error(w, http.StatusBadRequest, "The current password is wrong.") // the try counts
		return
	}
	s.logins.succeeded(reserved)
	if err := CheckPassword(req.NewPassword); err != nil {
		httpjson.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	hash, err := HashPassword(ctx, req.NewPassword)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if err := s.db.ChangePassword(ctx, user.ID, hash, session.ID); err != nil {
		s.internalError(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusOK, struct{}{})
}

// Answers for invite and reset links that cannot be used.
const (
	inviteUnknown = "This invite link is not valid. Check that you copied all of it."
	inviteGone    = "This invite has expired or was already used. Ask an admin for a new one."
	resetUnknown  = "This reset link is not valid. Check that you copied all of it."
	resetGone     = "This reset link has expired or was already used. Ask for a new one."
)

// findInvite looks up the invite of a token and answers 404 or 410 itself
// when there is none that can be used.
func (s *Service) findInvite(w http.ResponseWriter, r *http.Request, token string) (store.Invite, bool) {
	if token == "" {
		httpjson.Error(w, http.StatusNotFound, inviteUnknown)
		return store.Invite{}, false
	}
	inv, err := s.db.InviteByTokenHash(r.Context(), s.hashToken(token))
	switch {
	case errors.Is(err, store.ErrNotFound):
		httpjson.Error(w, http.StatusNotFound, inviteUnknown)
	case err != nil:
		s.internalError(w, r, err)
	case !inv.Usable(s.now()):
		httpjson.Error(w, http.StatusGone, inviteGone)
	default:
		return inv, true
	}
	return store.Invite{}, false
}

// tokenBody is the body of the check endpoints.
type tokenBody struct {
	Token string `json:"token"`
}

func (s *Service) inviteDetails(w http.ResponseWriter, r *http.Request) {
	var req tokenBody
	if !httpjson.Read(w, r, maxAuthBody, &req) {
		return
	}
	inv, ok := s.findInvite(w, r, req.Token)
	if !ok {
		return
	}
	httpjson.Write(w, http.StatusOK, map[string]string{"email": inv.Email, "role": inv.Role})
}

func (s *Service) acceptInvite(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token    string `json:"token"`
		Password string `json:"password"`
	}
	if !httpjson.Read(w, r, maxAuthBody, &req) {
		return
	}
	if _, ok := s.findInvite(w, r, req.Token); !ok {
		return
	}
	if err := CheckPassword(req.Password); err != nil {
		httpjson.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx := r.Context()
	hash, err := HashPassword(ctx, req.Password)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	// AcceptInvite checks the invite again inside its transaction: two
	// accepts at once cannot both succeed.
	u, err := s.db.AcceptInvite(ctx, s.hashToken(req.Token), hash, s.now())
	switch {
	case errors.Is(err, store.ErrNotFound):
		httpjson.Error(w, http.StatusNotFound, inviteUnknown)
	case errors.Is(err, store.ErrGone):
		httpjson.Error(w, http.StatusGone, inviteGone)
	case errors.Is(err, store.ErrEmailTaken):
		httpjson.Error(w, http.StatusConflict, "This email address already has an account. Sign in instead.")
	case err != nil:
		s.internalError(w, r, err)
	default:
		s.log.Info("auth: invite accepted", "user", u.ID, "role", u.Role)
		s.signIn(w, r, u)
	}
}

// forgotPassword mails a reset link when the address has an account and
// mail is on. It answers the same in every case, at once; the mail goes
// out in the background. Requests are throttled per address and client,
// so nobody can flood someone's inbox.
func (s *Service) forgotPassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email string `json:"email"`
	}
	if !httpjson.Read(w, r, maxAuthBody, &req) {
		return
	}
	email := store.NormalizeEmail(req.Email)
	if err := CheckEmail(email); err != nil {
		httpjson.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	// As for logins, an account's address is counted where a flood of
	// other addresses cannot push it out. Every request counts.
	_, err := s.db.UserByEmail(r.Context(), email)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		s.internalError(w, r, err)
		return
	}
	if _, wait, ok := s.resets.begin(email, err == nil, s.resets.clientKey(r), s.now()); !ok {
		tooManyAttempts(w, wait)
		return
	}
	if s.mailer != nil {
		s.mailForgotten(email)
	}
	httpjson.Write(w, http.StatusOK, struct{}{})
}

// findReset looks up the reset link of a token and answers 404 or 410
// itself when there is none that can be used.
func (s *Service) findReset(w http.ResponseWriter, r *http.Request, token string) (store.User, bool) {
	if token == "" {
		httpjson.Error(w, http.StatusNotFound, resetUnknown)
		return store.User{}, false
	}
	reset, u, err := s.db.PasswordResetByTokenHash(r.Context(), s.hashToken(token))
	switch {
	case errors.Is(err, store.ErrNotFound):
		httpjson.Error(w, http.StatusNotFound, resetUnknown)
	case err != nil:
		s.internalError(w, r, err)
	case !reset.Usable(s.now()):
		httpjson.Error(w, http.StatusGone, resetGone)
	default:
		return u, true
	}
	return store.User{}, false
}

func (s *Service) resetDetails(w http.ResponseWriter, r *http.Request) {
	var req tokenBody
	if !httpjson.Read(w, r, maxAuthBody, &req) {
		return
	}
	u, ok := s.findReset(w, r, req.Token)
	if !ok {
		return
	}
	httpjson.Write(w, http.StatusOK, map[string]string{"email": u.Email})
}

// resetPassword sets a new password with a reset link, signs the user out
// everywhere and signs them in here.
func (s *Service) resetPassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token    string `json:"token"`
		Password string `json:"password"`
	}
	if !httpjson.Read(w, r, maxAuthBody, &req) {
		return
	}
	if _, ok := s.findReset(w, r, req.Token); !ok {
		return
	}
	if err := CheckPassword(req.Password); err != nil {
		httpjson.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx := r.Context()
	hash, err := HashPassword(ctx, req.Password)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	u, err := s.db.UsePasswordReset(ctx, s.hashToken(req.Token), hash, s.now())
	switch {
	case errors.Is(err, store.ErrNotFound):
		httpjson.Error(w, http.StatusNotFound, resetUnknown)
	case errors.Is(err, store.ErrGone):
		httpjson.Error(w, http.StatusGone, resetGone)
	case err != nil:
		s.internalError(w, r, err)
	default:
		s.logins.forget(u.Email) // they proved they own the address
		s.log.Info("auth: password reset", "user", u.ID)
		s.signIn(w, r, u)
	}
}

func firstError(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}
