package bots

import "testing"

func TestBots_List(t *testing.T) {
	bots := []string{
		"Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)",
		"Mozilla/5.0 AppleWebKit/537.36 (KHTML, like Gecko; compatible; Googlebot/2.1; +http://www.google.com/bot.html) Chrome/141.0.7390.122 Safari/537.36",
		"Mozilla/5.0 (compatible; bingbot/2.0; +http://www.bing.com/bingbot.htm)",
		"DuckDuckBot/1.1; (+http://duckduckgo.com/duckduckbot.html)",
		"Mozilla/5.0 (compatible; Baiduspider/2.0; +http://www.baidu.com/search/spider.html)",
		"Mozilla/5.0 (compatible; YandexBot/3.0; +http://yandex.com/bots)",
		"Mozilla/5.0 (compatible; AhrefsBot/7.0; +http://ahrefs.com/robot/)",
		"Mozilla/5.0 (compatible; SemrushBot/7~bl; +http://www.semrush.com/bot.html)",
		"Mozilla/5.0 (compatible; MJ12bot/v1.4.8; http://mj12bot.com/)",
		"facebookexternalhit/1.1 (+http://www.facebook.com/externalhit_uatext.php)",
		"Twitterbot/1.0",
		"LinkedInBot/1.0 (compatible; Mozilla/5.0; Apache-HttpClient +http://www.linkedin.com)",
		"Slackbot-LinkExpanding 1.0 (+https://api.slack.com/robots)",
		"Mozilla/5.0 (compatible; Discordbot/2.0; +https://discordapp.com)",
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_5) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/13.1.1 Safari/605.1.15 (Applebot/0.1; +http://www.apple.com/go/applebot)",
		"Mozilla/5.0 (Linux; Android 7.0;) AppleWebKit/537.36 (KHTML, like Gecko) Mobile Safari/537.36 (compatible; PetalBot;+https://webmaster.petalsearch.com/site/petalbot)",
		"Mozilla/5.0 AppleWebKit/537.36 (KHTML, like Gecko; compatible; GPTBot/1.2; +https://openai.com/gptbot)",
		"Mozilla/5.0 AppleWebKit/537.36 (KHTML, like Gecko; compatible; ClaudeBot/1.0; +claudebot@anthropic.com)",
		"CCBot/2.0 (https://commoncrawl.org/faq/)",
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) HeadlessChrome/156.0.8078.4 Safari/537.36",
		// Anything that calls itself a bot, crawler or spider.
		"SomeNewBot/0.1",
		"example-crawler/2.0 (+https://example.com)",
		"Mozilla/5.0 (compatible; MySpider/1.0)",
	}
	for _, ua := range bots {
		if !IsBot(ua) {
			t.Errorf("IsBot(%q) = false, want true", ua)
		}
	}

	people := []string{
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36",
		"Mozilla/5.0 (iPhone; CPU iPhone OS 18_6 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.6 Mobile/15E148 Safari/604.1",
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:143.0) Gecko/20100101 Firefox/143.0",
		"Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Mobile Safari/537.36",
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36 Edg/141.0.0.0",
		"Mozilla/5.0 (X11; CrOS x86_64 14541.0.0) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36", // "CrOS" is no crawler
		"Mozilla/5.0 (Linux; Android 11; CUBOT P50) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Mobile Safari/537.36",
		"posthog-node/5.8.0",
		"",
	}
	for _, ua := range people {
		if IsBot(ua) {
			t.Errorf("IsBot(%q) = true, want false", ua)
		}
	}
}
