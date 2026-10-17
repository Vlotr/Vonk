package sentry

import "strings"

// Source tells which kind of app sent an event: "laravel" (the sentry-laravel
// SDK), "browser" (a JavaScript browser SDK) or else the platform the SDK
// reports in lowercase, such as "php" or "node"; "other" when there is none.
func Source(e *Event) string {
	if strings.HasPrefix(e.SDK.Name, "sentry.php.laravel") {
		return "laravel"
	}
	for _, p := range e.SDK.Packages {
		if p.Name == "composer:sentry/sentry-laravel" {
			return "laravel"
		}
	}
	// Browser SDKs (browser, react, vue, ...) report "javascript"; the server
	// SDKs for Node, Bun and Deno report "node".
	platform := strings.ToLower(e.Platform)
	if platform == "javascript" {
		return "browser"
	}
	if platform == "" {
		return "other"
	}
	return platform
}
