// Phase 4 starts with the user journey: on an error we see the pages the
// user visited before it. A visitor of the PostHog example (posthog-js and
// @sentry/browser, both told the same user id, an email address) signs in,
// sees three pages and pays, which fails. The issue page lists those pages
// in order with the time on each and the error last, and the release the
// issue was first seen in; "See all activity of this user" opens their page
// views, error and custom events. Once the hourly summaries are rebuilt
// (every minute), Events shows the shop's custom events with a chart.

import { expect, test } from '@playwright/test'
import { join } from 'node:path'
import { visitorContext } from './visitor'

const env = (name: string) => {
  const value = process.env[name]
  if (!value) throw new Error(`${name} is not set; global-setup.ts sets it`)
  return value
}

const screenshots = join(__dirname, 'screenshots')

test('the issue page shows the pages before the error; the user activity and Events follow', async ({ browser }) => {
  test.setTimeout(240_000)
  const monitor = env('MONITOR_URL')
  const shop = env('POSTHOG_EXAMPLE_URL')
  const projectId = env('PROJECT_ID')
  const user = 'anna.de.vries@example.com'
  const admin = await (await browser.newContext({ storageState: env('ADMIN_STATE') })).newPage()

  // Page views on for Webshop: phase 3 turns them on; run alone, this spec does.
  await admin.goto(`${monitor}/settings/projects`)
  const addPageViews = admin.locator('tbody tr').filter({ hasText: 'Webshop' }).getByRole('button', { name: 'Add page views' })
  if ((await addPageViews.count()) > 0) {
    await addPageViews.click()
    await expect(admin).toHaveURL(new RegExp(`/settings/projects/${projectId}/setup#page-views$`))
  }

  // Anna signs in on the home page, sees three pages and pays.
  const visitor = await (await visitorContext(browser)).newPage()
  await visitor.goto(`${shop}/?user=${encodeURIComponent(user)}`)
  await expect(visitor.getByText('posthog-js and the Sentry SDK are set up.')).toBeVisible()
  await visitor.waitForTimeout(2000)
  await visitor.getByRole('button', { name: `Sign in as ${user}` }).click()
  await visitor.getByRole('link', { name: 'Products' }).click()
  await visitor.waitForTimeout(2000)
  await visitor.getByRole('link', { name: 'Blue shoes' }).click() // history.pushState: a page view, and product_viewed
  await expect(visitor).toHaveURL(`${shop}/products/blue-shoes`)
  await visitor.waitForTimeout(3000)
  await visitor.getByRole('link', { name: 'Checkout' }).click()
  await visitor.waitForTimeout(4000)
  const sent = visitor.waitForResponse((r) => r.url().includes('/envelope/') && r.status() === 200)
  await visitor.getByRole('button', { name: 'Pay' }).click() // checkout_started, then the TypeError
  await sent
  await visitor.waitForTimeout(1000)
  await visitor.close({ runBeforeUnload: true }) // leaving: the time on /checkout

  // The issue of the error, from the issues of /checkout.
  await admin.goto(`${monitor}/projects/${projectId}/issues?route=%2Fcheckout`)
  const issue = admin.locator('tbody tr').filter({ hasText: 'TypeError' })
  await expect(issue).toHaveCount(1)
  await issue.getByRole('link').first().click()
  await expect(admin).toHaveURL(new RegExp(`/projects/${projectId}/issues/\\d+$`))

  // Before this error: the three pages in order, each with its time, and the error last.
  const card = admin.getByRole('region', { name: 'Before this error' })
  const steps = card.locator('li')
  await expect(async () => {
    await admin.reload()
    await expect(steps).toHaveCount(4, { timeout: 1000 })
    await expect(steps.nth(2)).toHaveText(/^3\/checkout\d+s$/, { timeout: 1000 }) // left the page: it has a time
  }).toPass({ timeout: 60_000, intervals: [2000] })
  await expect(card.getByText('Pages this user saw, from page tracking.')).toBeVisible()
  await expect(steps.nth(0)).toHaveText(/^1\/products\d+s$/)
  // /products/:slug with phase 3's route pattern; /products/blue-shoes when this spec runs alone.
  await expect(steps.nth(1)).toHaveText(/^2\/products\/(:slug|blue-shoes)\d+s$/)
  await expect(steps.nth(3)).toHaveText('Error on /checkout')

  // The user, the release and the link to all of their activity.
  const userId = admin.locator('dt', { hasText: /^User ID$/ }).locator('xpath=following-sibling::dd[1]')
  await expect(userId).toHaveText(user)
  const release = admin.locator('dt', { hasText: /^Release$/ }).locator('xpath=following-sibling::dd[1]')
  await expect(release).toHaveText('shop@2.8.1')
  await expect(release.getByRole('link')).toHaveAttribute('href', `/projects/${projectId}/issues?release=shop%402.8.1`)
  await admin.screenshot({ path: join(screenshots, 'phase4-journey.png'), fullPage: true })

  // All activity of this user: the error, the custom events and the pages, the newest first.
  await admin.getByRole('link', { name: 'See all activity of this user' }).click()
  await expect(admin).toHaveURL(`${monitor}/projects/${projectId}/users/anna.de.vries%40example.com`)
  await expect(admin.getByRole('heading', { level: 1 })).toHaveText(new RegExp(`^User ${user.replaceAll('.', '\\.')} was last seen (just now|\\d+ min ago)$`))
  const activity = admin.locator('li')
  await expect(activity.filter({ hasText: 'TypeError' })).toHaveCount(1)
  await expect(activity.filter({ hasText: 'checkout_started' })).toContainText('total: 49.95')
  await expect(activity.filter({ hasText: 'product_viewed' })).toContainText('product: blue-shoes')
  await expect(activity.filter({ hasText: '/products/blue-shoes' }).first()).toBeVisible()
  // Newest first: the error (or the event sent the same moment) on top, the first page last.
  await expect(activity.last()).toContainText('/products')
  await admin.screenshot({ path: join(screenshots, 'phase4-activity.png'), fullPage: true })
  // The error links back to its issue.
  await activity.filter({ hasText: 'TypeError' }).getByRole('link').click()
  await expect(admin.getByRole('region', { name: 'Before this error' })).toBeVisible()

  // Events, once the summaries are rebuilt (every minute).
  await admin.getByRole('navigation', { name: 'Webshop' }).getByRole('link', { name: 'Events' }).click()
  await expect(admin).toHaveURL(`${monitor}/projects/${projectId}/events`)
  const names = admin.locator('tbody tr td:first-child')
  await expect(async () => {
    await admin.reload()
    await expect(names).toHaveText(['checkout_started', 'product_viewed'], { timeout: 1000 })
  }).toPass({ timeout: 150_000, intervals: [5000] })
  // Phase 3's visit sent the same two events when the whole suite runs: once or twice each.
  await expect(admin.getByRole('heading', { level: 1 })).toHaveText(/^checkout_started happened (once|2 times) in the last 7\sdays$/) // a no-break space before days
  await expect(admin.locator('h1 + p')).toHaveText(/^By (1 user|2 users)\. Webshop, last 7 days\.$/)
  await expect(admin.getByRole('figure', { name: 'checkout_started: Times per day' })).toBeVisible()
  await expect(admin.getByRole('navigation', { name: 'Webshop' }).getByRole('link', { name: 'Events' })).toHaveAttribute('aria-current', 'page')
  await admin.screenshot({ path: join(screenshots, 'phase4-events.png'), fullPage: true })

  // Another event: its chart, and the choice in the URL.
  await admin.getByRole('button', { name: 'product_viewed' }).click()
  await expect(admin).toHaveURL(/[?&]event=product_viewed\b/)
  await expect(admin.getByRole('heading', { level: 1 })).toHaveText(/^product_viewed happened (once|2 times) in the last 7\sdays$/)
  await expect(admin.getByRole('figure', { name: 'product_viewed: Times per day' })).toBeVisible()
  await admin.getByRole('button', { name: '24 hours' }).click()
  await expect(admin).toHaveURL(/[?&]range=24h\b/)
  await expect(admin.getByText('Times per hour')).toBeVisible()
})
