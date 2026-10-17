package api

import "testing"

func TestParseUserAgent(t *testing.T) {
	tests := []struct {
		name, ua, browser, os string
	}{
		{"Chrome on a Mac", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/129.0.6668.58 Safari/537.36", "Chrome 129", "macOS"},
		{"headless Chrome on Linux", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) HeadlessChrome/156.0.8078.4 Safari/537.36", "Headless Chrome 156", "Linux"},
		{"Firefox on Windows", "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:131.0) Gecko/20100101 Firefox/131.0", "Firefox 131", "Windows"},
		{"Edge on Windows", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/129.0.0.0 Safari/537.36 Edg/129.0.2792.65", "Edge 129", "Windows"},
		{"Opera", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/128.0.0.0 Safari/537.36 OPR/114.0.0.0", "Opera 114", "Windows"},
		{"Safari on a Mac", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.4.1 Safari/605.1.15", "Safari 17", "macOS"},
		{"Safari on an iPhone", "Mozilla/5.0 (iPhone; CPU iPhone OS 17_4 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.4 Mobile/15E148 Safari/604.1", "Safari 17", "iOS 17"},
		{"Chrome on an iPad", "Mozilla/5.0 (iPad; CPU OS 17_4 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) CriOS/129.0.6668.69 Mobile/15E148 Safari/604.1", "Chrome 129", "iPadOS 17"},
		{"Chrome on Android", "Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/129.0.6668.81 Mobile Safari/537.36", "Chrome 129", "Android 14"},
		{"Samsung Internet", "Mozilla/5.0 (Linux; Android 13; SM-S911B) AppleWebKit/537.36 (KHTML, like Gecko) SamsungBrowser/25.0 Chrome/121.0.0.0 Mobile Safari/537.36", "Samsung Internet 25", "Android 13"},
		{"Firefox on an iPhone", "Mozilla/5.0 (iPhone; CPU iPhone OS 17_4 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) FxiOS/131.0 Mobile/15E148 Safari/605.1.15", "Firefox 131", "iOS 17"},
		{"ChromeOS", "Mozilla/5.0 (X11; CrOS x86_64 14541.0.0) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/129.0.0.0 Safari/537.36", "Chrome 129", "ChromeOS"},
		{"curl", "curl/8.7.1", "", ""},
		{"empty", "", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			browser, os := parseUserAgent(tt.ua)
			if browser != tt.browser || os != tt.os {
				t.Errorf("parseUserAgent = %q, %q; want %q, %q", browser, os, tt.browser, tt.os)
			}
		})
	}
}
