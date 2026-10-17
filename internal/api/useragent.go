package api

import (
	"regexp"
	"strings"
)

// browserMarkers are User-Agent tokens in the order they are checked: most
// browsers also name Chrome or Safari, so the specific ones come first.
var browserMarkers = []struct{ token, name string }{
	{"Edg/", "Edge"},
	{"EdgA/", "Edge"},
	{"EdgiOS/", "Edge"},
	{"OPR/", "Opera"},
	{"SamsungBrowser/", "Samsung Internet"},
	{"Firefox/", "Firefox"},
	{"FxiOS/", "Firefox"},
	{"HeadlessChrome/", "Headless Chrome"},
	{"CriOS/", "Chrome"},
	{"Chrome/", "Chrome"},
	{"Version/", "Safari"}, // Safari puts its own version after "Version/"
}

var (
	iosVersion     = regexp.MustCompile(` OS (\d+)[_\d]* like Mac OS X`)
	androidVersion = regexp.MustCompile(`Android (\d+)`)
)

// parseUserAgent names the browser and operating system in a User-Agent
// header, such as "Chrome 129" and "macOS"; each is "" when unknown. Versions
// are major versions only. macOS and Windows have none: browsers froze them
// in the header (every Mac says 10.15.7), so they would be wrong.
func parseUserAgent(ua string) (browser, os string) {
	for _, m := range browserMarkers {
		i := strings.Index(ua, m.token)
		if i < 0 || (m.name == "Safari" && !strings.Contains(ua, "Safari/")) {
			continue
		}
		browser = m.name
		if v := majorVersion(ua[i+len(m.token):]); v != "" {
			browser += " " + v
		}
		break
	}

	switch {
	case strings.Contains(ua, "like Mac OS X"):
		os = "iOS"
		if strings.Contains(ua, "iPad") {
			os = "iPadOS"
		}
		if m := iosVersion.FindStringSubmatch(ua); m != nil {
			os += " " + m[1]
		}
	case strings.Contains(ua, "Mac OS X"):
		os = "macOS"
	case strings.Contains(ua, "Windows"):
		os = "Windows"
	case strings.Contains(ua, "Android"):
		os = "Android"
		if m := androidVersion.FindStringSubmatch(ua); m != nil {
			os += " " + m[1]
		}
	case strings.Contains(ua, "CrOS"):
		os = "ChromeOS"
	case strings.Contains(ua, "Linux"):
		os = "Linux"
	}
	return browser, os
}

// majorVersion returns the leading digits of s: "129" for "129.0.6668.58 Safari".
func majorVersion(s string) string {
	end := 0
	for end < len(s) && s[end] >= '0' && s[end] <= '9' {
		end++
	}
	return s[:end]
}
