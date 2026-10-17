// A visitor's browser for the PostHog example, shared by the specs that
// browse it (phase 3 and 4).

import type { Browser } from '@playwright/test'

/** chrome is a visitor's Chrome on macOS, as the PostHog fixtures were recorded with. */
export const chrome = 'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36'

/**
 * visitorContext is a browser whose requests carry userAgent. posthog-js
 * sends nothing from a browser that looks automated or calls itself a bot
 * in navigator.userAgent, so the page always sees a visitor's Chrome; the
 * monitor tells a bot by the User-Agent of its requests. The page's own
 * navigator.userAgent getter matters: with only the context's userAgent
 * set, posthog-js sent no page leave when a link opened another page
 * (Task 9), so pages left that way had no time.
 */
export async function visitorContext(browser: Browser, userAgent: string = chrome) {
  const context = await browser.newContext({ userAgent })
  await context.addInitScript((pageUserAgent) => {
    Object.defineProperty(Navigator.prototype, 'webdriver', { get: () => false })
    Object.defineProperty(Navigator.prototype, 'userAgentData', { get: () => undefined })
    Object.defineProperty(Navigator.prototype, 'userAgent', { get: () => pageUserAgent })
  }, chrome)
  return context
}
