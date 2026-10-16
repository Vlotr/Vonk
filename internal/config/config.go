// Package config reads the settings from environment variables.
//
// Required: SECRET_KEY and BASE_URL. Optional: DATABASE_URL, MAIL_URL, ADDR,
// DATA_DIR, LOG_LEVEL and DEV_BASIC_AUTH. Load reports every problem at once,
// so an operator can fix the whole environment in one go.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"path/filepath"
	"strings"
)

// Defaults for the optional settings.
const (
	DefaultAddr    = ":8000"
	DefaultDataDir = "/data"
)

// minSecretKeyLength is the shortest SECRET_KEY we accept.
const minSecretKeyLength = 16

// placeholderSecrets are example values from the docs that must never run in production.
var placeholderSecrets = []string{"verander-mij", "change-me"}

// Config holds every setting the tool reads at startup.
type Config struct {
	// SecretKey protects sessions and keys.
	SecretKey string
	// BaseURL is the public root of the tool, without a trailing slash,
	// for example "https://monitor.example.com". SDK DSNs are built from it.
	BaseURL string
	// DatabaseURL selects Postgres instead of SQLite when set.
	DatabaseURL string
	// MailURL is the SMTP server for notifications; empty means Slack only.
	MailURL string
	// Addr is the listen address of the HTTP server.
	Addr string
	// DataDir holds the SQLite database and backups.
	DataDir string
	// SQLitePath is the SQLite database file. Empty when DatabaseURL is set.
	SQLitePath string
	// LogLevel is the minimum level that is logged.
	LogLevel slog.Level
	// DevBasicAuth is "user:pass" for the temporary dashboard guard, or empty.
	DevBasicAuth string
}

// Load reads the configuration through getenv (pass os.Getenv in production).
func Load(getenv func(string) string) (Config, error) {
	cfg := Config{
		SecretKey:    getenv("SECRET_KEY"),
		DatabaseURL:  strings.TrimSpace(getenv("DATABASE_URL")),
		MailURL:      strings.TrimSpace(getenv("MAIL_URL")),
		Addr:         withDefault(getenv("ADDR"), DefaultAddr),
		DataDir:      withDefault(getenv("DATA_DIR"), DefaultDataDir),
		DevBasicAuth: getenv("DEV_BASIC_AUTH"),
	}

	var problems []error

	if err := checkSecretKey(cfg.SecretKey); err != nil {
		problems = append(problems, err)
	}

	baseURL, err := parseBaseURL(getenv("BASE_URL"))
	if err != nil {
		problems = append(problems, err)
	}
	cfg.BaseURL = baseURL

	if err := checkDatabaseURL(cfg.DatabaseURL); err != nil {
		problems = append(problems, err)
	}
	if cfg.DatabaseURL == "" {
		cfg.SQLitePath = filepath.Join(cfg.DataDir, "monitor.db")
	}

	if v := strings.TrimSpace(getenv("LOG_LEVEL")); v != "" {
		if err := cfg.LogLevel.UnmarshalText([]byte(v)); err != nil {
			problems = append(problems, fmt.Errorf("LOG_LEVEL must be debug, info, warn or error, got %q", v))
		}
	}

	if cfg.DevBasicAuth != "" {
		user, pass, ok := strings.Cut(cfg.DevBasicAuth, ":")
		if !ok || user == "" || pass == "" {
			problems = append(problems, errors.New("DEV_BASIC_AUTH must look like user:password"))
		}
	}

	if len(problems) > 0 {
		return Config{}, errors.Join(problems...)
	}
	return cfg, nil
}

func withDefault(value, fallback string) string {
	if v := strings.TrimSpace(value); v != "" {
		return v
	}
	return fallback
}

func checkSecretKey(key string) error {
	if key == "" {
		return errors.New("SECRET_KEY is required: set it to a long random string, for example the output of `openssl rand -hex 32`")
	}
	for _, placeholder := range placeholderSecrets {
		if strings.EqualFold(strings.TrimSpace(key), placeholder) {
			return fmt.Errorf("SECRET_KEY is still the example value %q: set it to a long random string", placeholder)
		}
	}
	if len(key) < minSecretKeyLength {
		return fmt.Errorf("SECRET_KEY must be at least %d characters long", minSecretKeyLength)
	}
	return nil
}

// parseBaseURL checks that raw is an http(s) URL of a host root and returns
// it without a trailing slash. Hosting under a subpath is not supported.
func parseBaseURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("BASE_URL is required: set it to the public address of the tool, for example https://monitor.example.com")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("BASE_URL %q is not a valid URL: %w", raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("BASE_URL %q must start with http:// or https://", raw)
	}
	if u.Host == "" {
		return "", fmt.Errorf("BASE_URL %q has no host", raw)
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.ForceQuery {
		return "", errors.New("BASE_URL must not contain a user, query or fragment")
	}
	if u.Path != "" && u.Path != "/" {
		return "", fmt.Errorf("BASE_URL %q must be the root of a host (no path such as %q); hosting under a subpath is not supported", raw, u.Path)
	}
	return u.Scheme + "://" + u.Host, nil
}

func checkDatabaseURL(raw string) error {
	if raw == "" {
		return nil
	}
	if strings.HasPrefix(raw, "postgres://") || strings.HasPrefix(raw, "postgresql://") {
		return nil
	}
	return errors.New("DATABASE_URL must start with postgres:// or postgresql://; leave it empty to use SQLite")
}
