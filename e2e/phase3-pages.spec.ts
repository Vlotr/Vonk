// Phase 3 is page views. The admin turns them on for Webshop ("Add page
// views" opens Setup and keys with the posthog-js setup) and adds a route
// pattern on Edit project. A visitor browses the PostHog example (posthog-js
// and @sentry/browser, signed in as user 42 in both), stays a few seconds on
// each page, pays (which fails) and leaves; a bot visits too. Once the
// hourly summaries are rebuilt (every minute), Pages shows each route with
// its views and average time, the bot counted apart, and the checkout's
// error links to the issues of that page.

import { expect, test } from '@playwright/test'
import { join } from 'node:path'
import { chrome, visitorContext } from './visitor'

const env = (name: string) => {
  const value = process.env[name]
  if (!value) throw new Error(`${name} is not set; global-setup.ts sets it`)
  return value
}

const screenshots = join(__dirname, 'screenshots')

const googlebot = 'Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)'

test('Pages shows the views and average time per page, with bots apart and errors per page', async ({ browser }) => {
  test.setTimeout(240_000)
  const monitor = env('MONITOR_URL')
  const shop = env('POSTHOG_EXAMPLE_URL')
  const projectId = env('PROJECT_ID')
  const admin = await (await browser.newContext({ storageState: env('ADMIN_STATE') })).newPage()

  // Add page views: on, and Setup and keys shows the posthog-js setup.
  await admin.goto(`${monitor}/settings/projects`)
  await admin.locator('tbody tr').filter({ hasText: 'Webshop' }).getByRole('button', { name: 'Add page views' }).click()
  await expect(admin).toHaveURL(new RegExp(`/settings/projects/${projectId}/setup#page-views$`))
  const setup = admin.locator('pre[aria-label="PostHog setup"]')
  await expect(setup).toContainText(`api_host: '${monitor}'`)
  for (const line of ["persistence: 'memory'", "capture_pageview: 'history_change'", 'capture_pageleave: true', 'disable_session_recording: true']) {
    await expect(setup).toContainText(line)
  }
  await expect(admin.locator('pre[aria-label="Browser user id"]')).toContainText('posthog.identify(String(user.id))')
  await expect(admin.locator('pre[aria-label="Laravel user id"]')).toContainText('setUser')

  // A route pattern, for page views from now on.
  await admin.goto(`${monitor}/settings/projects?edit=${projectId}`)
  await expect(admin.getByText('Patterns apply to page views and errors that come in from now on.')).toBeVisible()
  await admin.getByLabel('Route patterns').fill('/products/:slug')
  await admin.getByRole('button', { name: 'Save changes' }).click()
  await expect(admin.getByRole('heading', { name: 'Edit Webshop' })).toHaveCount(0)

  // A visitor: a few seconds on each page, signed in after the first.
  const visitor = await (await visitorContext(browser, chrome)).newPage()
  await visitor.goto(`${shop}/`)
  await expect(visitor.getByText('posthog-js and the Sentry SDK are set up.')).toBeVisible()
  await visitor.waitForTimeout(2000)
  await visitor.getByRole('button', { name: 'Sign in as user 42' }).click()
  await visitor.getByRole('link', { name: 'Products' }).click()
  await visitor.waitForTimeout(2000)
  await visitor.getByRole('link', { name: 'Blue shoes' }).click() // history.pushState: a page view of its own
  await expect(visitor).toHaveURL(`${shop}/products/blue-shoes`)
  await visitor.waitForTimeout(3000)
  await visitor.getByRole('link', { name: 'Checkout' }).click()
  await visitor.waitForTimeout(4000)
  const sent = visitor.waitForResponse((r) => r.url().includes('/envelope/') && r.status() === 200)
  await visitor.getByRole('button', { name: 'Pay' }).click()
  await sent
  await visitor.close({ runBeforeUnload: true }) // leaving: the page leave of /checkout

  // A bot looks at two pages.
  const bot = await (await visitorContext(browser, googlebot)).newPage()
  await bot.goto(`${shop}/`)
  await bot.waitForTimeout(1500)
  await bot.getByRole('link', { name: 'Products' }).click()
  await bot.waitForTimeout(1500)
  await bot.close({ runBeforeUnload: true })

  // Pages, once the summaries are rebuilt (every minute).
  await admin.goto(`${monitor}/projects/${projectId}/pages`)
  const rows = admin.locator('tbody tr')
  const routes = rows.locator('td:first-child')
  await expect(async () => {
    await admin.reload()
    await expect(routes).toHaveText(['/', '/checkout', '/products', '/products/:slug'], { timeout: 1000 })
    await expect(admin.getByText('2 bot visits not counted')).toBeVisible({ timeout: 1000 })
    await expect(rows.filter({ hasText: 'No time yet' })).toHaveCount(0, { timeout: 1000 })
  }).toPass({ timeout: 150_000, intervals: [5000] })

  await expect(admin.getByRole('heading', { level: 1 })).toHaveText('Visitors stay longest on /checkout')
  // The sentence names the page with the most errors: /checkout has one; /
  // has the browser example's two from phase 1 when the whole suite runs.
  await expect(admin.locator('h1 + p')).toHaveText(/^(It is also the page with the most errors|The most errors happen on \/)\. Webshop, last 7 days\.$/)
  const totals = admin.getByRole('region', { name: 'Totals' })
  await expect(totals.locator('dd').nth(0)).toHaveText('4') // the bot's two are not in it
  await expect(totals.locator('dd').nth(1)).toHaveText('2') // anonymous on the first page, then user 42
  await expect(totals.locator('dd').nth(2)).toHaveText(/^\d+s$/)
  for (const row of await rows.all()) {
    await expect(row.locator('td').nth(1)).toHaveText('1')
    await expect(row.locator('td').nth(3)).toHaveText(/^\d+s$/)
  }
  await expect(admin.getByRole('navigation', { name: 'Webshop' }).getByRole('link', { name: 'Pages' })).toHaveAttribute('aria-current', 'page')
  await admin.screenshot({ path: join(screenshots, 'phase3-pages.png'), fullPage: true })

  // Other periods have the same visits.
  await admin.getByRole('button', { name: '24 hours' }).click()
  await expect(admin).toHaveURL(/[?&]range=24h\b/)
  await expect(admin.getByText('Page views per hour')).toBeVisible()
  await expect(totals.locator('dd').nth(0)).toHaveText('4')

  // The checkout's error leads to the issues of that page, with user 42.
  await rows.filter({ hasText: '/checkout' }).getByRole('link').click()
  await expect(admin).toHaveURL(new RegExp(`/projects/${projectId}/issues\\?route=%2Fcheckout$`))
  await expect(admin.getByText('Only errors on the page')).toContainText('/checkout')
  const issue = admin.locator('tbody tr').filter({ hasText: 'TypeError' })
  await expect(issue).toHaveCount(1)
  await issue.getByRole('link').click()
  await expect(admin.getByText("Cannot read properties of undefined (reading 'price')").first()).toBeVisible()
  const userId = admin.locator('dt', { hasText: /^User ID$/ }).locator('xpath=following-sibling::dd[1]')
  await expect(userId).toHaveText('42')

  // All projects shows the page views of the last 24 hours and their time.
  await admin.goto(monitor)
  const webshop = admin.locator('tbody tr').filter({ hasText: 'Webshop' })
  await expect(webshop.locator('td').nth(4)).toHaveText('4')
  await expect(webshop.locator('td').nth(5)).toHaveText(/^\dm \d\ds$/)

  // Edit project previews recent paths with the routes they get.
  await admin.goto(`${monitor}/settings/projects?edit=${projectId}`)
  await expect(admin.getByRole('row', { name: '/products/blue-shoes /products/:slug' })).toBeVisible()
})
