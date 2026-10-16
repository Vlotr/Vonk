package auth

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"monitor/internal/config"
	"monitor/internal/httpjson"
	"monitor/internal/mail"
	"monitor/internal/store"
)

// TestMain makes new password hashes cheap: the tests hash many passwords
// and run with -race. Verification reads the cost from each hash.
func TestMain(m *testing.M) {
	hashParams = argonParams{memory: 1024, time: 1, threads: 1, keyLen: 32, saltLen: 16}
	os.Exit(m.Run())
}

var authTestStart = time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)

// fakeMailer records the mail the service sends.
type fakeMailer struct {
	mu   sync.Mutex
	sent []mail.Message
}

func (f *fakeMailer) Send(_ context.Context, msg mail.Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, msg)
	return nil
}

func (f *fakeMailer) messages() []mail.Message {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]mail.Message(nil), f.sent...)
}

// authEnv is a service on a fresh database, with a clock the test moves,
// behind the same routing the server uses: public auth routes, and the
// rest behind RequireUser.
type authEnv struct {
	db      *store.DB
	svc     *Service
	mailer  *fakeMailer
	handler http.Handler

	mu  sync.Mutex
	now time.Time
}

type envOption func(*Options)

// withoutMail runs the service as if MAIL_URL were not set.
func withoutMail(o *Options) { o.Mailer = nil }

func newAuthEnv(t *testing.T, opts ...envOption) *authEnv {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(context.Background(), config.Config{DataDir: dir, SQLitePath: filepath.Join(dir, "monitor.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}

	env := &authEnv{db: db, mailer: &fakeMailer{}, now: authTestStart}
	o := Options{
		DB:        db,
		SecretKey: "a-long-random-secret-for-tests",
		BaseURL:   "https://monitor.example.com",
		Mailer:    env.mailer,
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now:       env.clock,
	}
	for _, opt := range opts {
		opt(&o)
	}
	env.svc = New(o)

	guarded := http.NewServeMux()
	env.svc.Register(guarded)
	// Stand-ins for the dashboard API and pages.
	guarded.HandleFunc("GET /ui/api/ping", func(w http.ResponseWriter, r *http.Request) {
		u := UserFrom(r.Context())
		httpjson.Write(w, http.StatusOK, map[string]any{"email": u.Email, "projects": AccessibleProjectIDs(r.Context(), u)})
	})
	guarded.HandleFunc("POST /ui/api/ping", func(w http.ResponseWriter, r *http.Request) {
		httpjson.Write(w, http.StatusOK, map[string]string{"changed": "yes"})
	})
	guarded.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "dashboard page") })

	public := http.NewServeMux()
	env.svc.RegisterPublic(public)
	public.Handle("/", env.svc.RequireUser(guarded))
	env.handler = public
	return env
}

func (e *authEnv) clock() time.Time {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.now
}

func (e *authEnv) advance(d time.Duration) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.now = e.now.Add(d)
}

// browser is one client: its session cookie, CSRF token and address.
type browser struct {
	cookie *http.Cookie
	csrf   string
	remote string
}

func newBrowser() *browser { return &browser{remote: "203.0.113.10:5000"} }

// do sends a request as b. A body is sent as JSON. Cookies the server sets
// are kept, as a browser does.
func (e *authEnv) do(b *browser, method, path, body string) *httptest.ResponseRecorder {
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, r)
	req.RemoteAddr = b.remote
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if b.cookie != nil {
		req.AddCookie(b.cookie)
	}
	if b.csrf != "" {
		req.Header.Set("X-CSRF-Token", b.csrf)
	}
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	for _, c := range rec.Result().Cookies() {
		if c.Name == e.svc.cookieName {
			if c.MaxAge < 0 {
				b.cookie = nil
			} else {
				b.cookie = c
			}
		}
	}
	return rec
}

// meResponse is GET /ui/api/me and the answer of every sign-in.
type meResponse struct {
	ID          int64  `json:"id"`
	Email       string `json:"email"`
	Role        string `json:"role"`
	CSRFToken   string `json:"csrfToken"`
	MailEnabled bool   `json:"mailEnabled"`
}

// signedIn reads a sign-in answer, expects 200 and keeps the CSRF token in b.
func signedIn(t *testing.T, b *browser, rec *httptest.ResponseRecorder) meResponse {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("sign-in = %d %s", rec.Code, rec.Body)
	}
	var me meResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &me); err != nil {
		t.Fatalf("decode %s: %v", rec.Body, err)
	}
	if me.CSRFToken == "" || b.cookie == nil {
		t.Fatalf("sign-in gave no CSRF token or no cookie: %s", rec.Body)
	}
	b.csrf = me.CSRFToken
	return me
}

// setup creates the first admin through the API and returns a browser signed in as them.
func (e *authEnv) setup(t *testing.T, email, password string) *browser {
	t.Helper()
	b := newBrowser()
	signedIn(t, b, e.do(b, "POST", "/ui/api/setup", jsonBody(map[string]string{"email": email, "password": password})))
	return b
}

// login signs in a new browser.
func (e *authEnv) login(t *testing.T, email, password string) *browser {
	t.Helper()
	b := newBrowser()
	signedIn(t, b, e.do(b, "POST", "/ui/api/login", jsonBody(map[string]string{"email": email, "password": password})))
	return b
}

func jsonBody(v any) string {
	data, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(data)
}

// errorOf returns the "error" field of a JSON error answer.
func errorOf(rec *httptest.ResponseRecorder) string {
	var body struct {
		Error string `json:"error"`
	}
	json.Unmarshal(rec.Body.Bytes(), &body)
	return body.Error
}

// tokenFrom returns the token= value of the first link to path in text.
func tokenFrom(t *testing.T, text, path string) string {
	t.Helper()
	prefix := "https://monitor.example.com" + path + "?token="
	i := strings.Index(text, prefix)
	if i < 0 {
		t.Fatalf("no %s link in %q", prefix, text)
	}
	token := text[i+len(prefix):]
	if end := strings.IndexAny(token, " \n\r"); end >= 0 {
		token = token[:end]
	}
	return token
}
