// Package bots recognizes crawlers and other automated visitors by their
// User-Agent header. Their page views are stored, marked as bots, and
// counted apart from people's.
package bots

import "strings"

// known are names of well-known bots, lowercase, matched anywhere in the
// User-Agent. Most also contain "bot", "crawler" or "spider", which the
// generic rule catches; they are listed so the list says what it covers.
var known = []string{
	"googlebot", "bingbot", "duckduckbot", "baiduspider", "yandexbot", "ahrefsbot", "semrushbot", "mj12bot",
	"facebookexternalhit", "twitterbot", "linkedinbot", "slackbot", "discordbot", "applebot", "petalbot",
	"gptbot", "claudebot", "ccbot", "headlesschrome",
}

// generic are words that bots use to describe themselves.
var generic = []string{"bot", "crawler", "spider"}

// notBots are names that contain a generic word but are people's devices
// (the Cubot phones).
var notBots = []string{"cubot"}

// IsBot reports whether userAgent belongs to a known bot, or to anything
// that calls itself a bot, crawler or spider. Case is ignored.
func IsBot(userAgent string) bool {
	ua := strings.ToLower(userAgent)
	for _, name := range known {
		if strings.Contains(ua, name) {
			return true
		}
	}
	for _, name := range notBots {
		ua = strings.ReplaceAll(ua, name, "")
	}
	for _, word := range generic {
		if strings.Contains(ua, word) {
			return true
		}
	}
	return false
}
