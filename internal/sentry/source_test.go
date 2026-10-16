package sentry_test

import (
	"testing"

	"monitor/internal/sentry"
)

func TestSource(t *testing.T) {
	cases := map[string]string{
		`{"platform":"php","sdk":{"name":"sentry.php.laravel","version":"4.20.0"}}`:                             "laravel",
		`{"platform":"php","sdk":{"name":"sentry.php","packages":[{"name":"composer:sentry/sentry-laravel"}]}}`: "laravel",
		`{"platform":"javascript","sdk":{"name":"sentry.javascript.browser"}}`:                                  "browser",
		`{"platform":"javascript","sdk":{"name":"sentry.javascript.react"}}`:                                    "browser",
		`{"platform":"php","sdk":{"name":"sentry.php"}}`:                                                        "php",
		`{"platform":"node","sdk":{"name":"sentry.javascript.node"}}`:                                           "node",
		`{"platform":"Python"}`: "python",
		`{}`:                    "other",
	}
	for in, want := range cases {
		if got := sentry.Source(decode(t, in)); got != want {
			t.Errorf("Source(%s) = %q, want %q", in, got, want)
		}
	}
}

func TestSource_Fixtures(t *testing.T) {
	if got := sentry.Source(parse(t, fixture(t, "browser-typeerror.envelope")).Events[0]); got != "browser" {
		t.Errorf("browser fixture: Source = %q", got)
	}
	// The fixture comes from plain sentry/sentry, not sentry-laravel.
	if got := sentry.Source(parse(t, fixture(t, "php-exception.envelope.gz")).Events[0]); got != "php" {
		t.Errorf("PHP fixture: Source = %q", got)
	}
}
