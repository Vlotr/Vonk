package config

import (
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
)

// env returns a getenv function backed by a map, so tests never touch the real environment.
func env(vars map[string]string) func(string) string {
	return func(key string) string { return vars[key] }
}

// validEnv holds the smallest set of variables that loads without error.
func validEnv() map[string]string {
	return map[string]string{
		"SECRET_KEY": "a-long-random-secret-for-tests",
		"BASE_URL":   "https://monitor.example.com",
	}
}

func TestLoadConfig_RequiresSecretKeyAndBaseURL(t *testing.T) {
	for _, name := range []string{"SECRET_KEY", "BASE_URL"} {
		t.Run(name, func(t *testing.T) {
			vars := validEnv()
			delete(vars, name)

			_, err := Load(env(vars))
			if err == nil {
				t.Fatalf("Load without %s: want error, got nil", name)
			}
			if !strings.Contains(err.Error(), name) {
				t.Errorf("error %q does not name %s", err, name)
			}
		})
	}
}

func TestLoadConfig_RejectsPlaceholderSecret(t *testing.T) {
	for _, secret := range []string{"verander-mij", "change-me", "CHANGE-ME", "short-secret"} {
		t.Run(secret, func(t *testing.T) {
			vars := validEnv()
			vars["SECRET_KEY"] = secret

			_, err := Load(env(vars))
			if err == nil {
				t.Fatalf("Load with SECRET_KEY=%q: want error, got nil", secret)
			}
			if !strings.Contains(err.Error(), "SECRET_KEY") {
				t.Errorf("error %q does not name SECRET_KEY", err)
			}
		})
	}
}

func TestLoadConfig_Defaults(t *testing.T) {
	cfg, err := Load(env(validEnv()))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.Addr != ":8000" {
		t.Errorf("Addr = %q, want :8000", cfg.Addr)
	}
	if cfg.DataDir != "/data" {
		t.Errorf("DataDir = %q, want /data", cfg.DataDir)
	}
	if cfg.DatabaseURL != "" {
		t.Errorf("DatabaseURL = %q, want empty", cfg.DatabaseURL)
	}
	if want := filepath.Join("/data", "monitor.db"); cfg.SQLitePath != want {
		t.Errorf("SQLitePath = %q, want %q", cfg.SQLitePath, want)
	}
	if cfg.LogLevel != slog.LevelInfo {
		t.Errorf("LogLevel = %v, want info", cfg.LogLevel)
	}
	if cfg.DevBasicAuth != "" {
		t.Errorf("DevBasicAuth = %q, want empty", cfg.DevBasicAuth)
	}
}

func TestLoadConfig_DataDirMovesSQLiteFile(t *testing.T) {
	vars := validEnv()
	vars["DATA_DIR"] = "/srv/monitor"

	cfg, err := Load(env(vars))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if want := filepath.Join("/srv/monitor", "monitor.db"); cfg.SQLitePath != want {
		t.Errorf("SQLitePath = %q, want %q", cfg.SQLitePath, want)
	}
}

func TestLoadConfig_BaseURLMustBeRoot(t *testing.T) {
	ok := map[string]string{
		"https://monitor.example.com":       "https://monitor.example.com",
		"https://monitor.example.com/":      "https://monitor.example.com",
		"http://localhost:8000":             "http://localhost:8000",
		"https://Monitor.Example.com:8443/": "https://Monitor.Example.com:8443",
	}
	for in, want := range ok {
		t.Run("accepts "+in, func(t *testing.T) {
			vars := validEnv()
			vars["BASE_URL"] = in

			cfg, err := Load(env(vars))
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if cfg.BaseURL != want {
				t.Errorf("BaseURL = %q, want %q", cfg.BaseURL, want)
			}
		})
	}

	bad := []string{
		"https://example.com/monitor",
		"https://example.com/monitor/",
		"https://example.com/?x=1",
		"https://example.com/#top",
		"monitor.example.com",
		"ftp://monitor.example.com",
		"https://",
	}
	for _, in := range bad {
		t.Run("rejects "+in, func(t *testing.T) {
			vars := validEnv()
			vars["BASE_URL"] = in

			_, err := Load(env(vars))
			if err == nil {
				t.Fatalf("Load with BASE_URL=%q: want error, got nil", in)
			}
			if !strings.Contains(err.Error(), "BASE_URL") {
				t.Errorf("error %q does not name BASE_URL", err)
			}
		})
	}
}

func TestLoadConfig_BaseURLErrorsHideCredentials(t *testing.T) {
	bad := []string{
		"ftp://admin:secret-pass@monitor.example.com",       // wrong scheme
		"https://admin:secret-pass@monitor.example.com",     // user info
		"https://admin:secret-pass@monitor.example.com:x",   // does not parse
		"https://admin:secret-pass@monitor.example.com/sub", // path
		"admin:secret-pass@monitor.example.com",             // no scheme
		"https://admin:secret-pass@",                        // no host
	}
	for _, in := range bad {
		t.Run(in, func(t *testing.T) {
			vars := validEnv()
			vars["BASE_URL"] = in

			_, err := Load(env(vars))
			if err == nil {
				t.Fatalf("Load with BASE_URL=%q: want error, got nil", in)
			}
			if !strings.Contains(err.Error(), "BASE_URL") {
				t.Errorf("error %q does not name BASE_URL", err)
			}
			if strings.Contains(err.Error(), "secret-pass") {
				t.Errorf("error %q shows the password", err)
			}
		})
	}
}

func TestLoadConfig_OptionalValues(t *testing.T) {
	vars := validEnv()
	vars["ADDR"] = "127.0.0.1:9000"
	vars["LOG_LEVEL"] = "debug"
	vars["DEV_BASIC_AUTH"] = "user:pass"
	vars["MAIL_URL"] = "smtp://user:pass@mail.example.com:587"
	vars["DATABASE_URL"] = "postgres://monitor@db/monitor"

	cfg, err := Load(env(vars))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Addr != "127.0.0.1:9000" {
		t.Errorf("Addr = %q", cfg.Addr)
	}
	if cfg.LogLevel != slog.LevelDebug {
		t.Errorf("LogLevel = %v, want debug", cfg.LogLevel)
	}
	if cfg.DevBasicAuth != "user:pass" {
		t.Errorf("DevBasicAuth = %q", cfg.DevBasicAuth)
	}
	if cfg.MailURL != vars["MAIL_URL"] {
		t.Errorf("MailURL = %q", cfg.MailURL)
	}
	if cfg.DatabaseURL != vars["DATABASE_URL"] {
		t.Errorf("DatabaseURL = %q", cfg.DatabaseURL)
	}
	if cfg.SQLitePath != "" {
		t.Errorf("SQLitePath = %q, want empty when DATABASE_URL is set", cfg.SQLitePath)
	}
}

func TestLoadConfig_RejectsBadOptionalValues(t *testing.T) {
	cases := map[string]string{
		"LOG_LEVEL":      "loud",
		"DEV_BASIC_AUTH": "no-colon",
		"DATABASE_URL":   "mysql://db/monitor",
	}
	for name, value := range cases {
		t.Run(name, func(t *testing.T) {
			vars := validEnv()
			vars[name] = value

			_, err := Load(env(vars))
			if err == nil {
				t.Fatalf("Load with %s=%q: want error, got nil", name, value)
			}
			if !strings.Contains(err.Error(), name) {
				t.Errorf("error %q does not name %s", err, name)
			}
		})
	}
}
