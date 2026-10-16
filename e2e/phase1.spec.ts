// Phase 1 is done when a real error from a Laravel app, sent with the
// official sentry/sentry-laravel SDK, shows up in the dashboard's issue list.
// This test proves it in a browser, together with the browser SDK.

import { expect, type Page, test } from '@playwright/test'
import { join } from 'node:path'

const env = (name: string) => {
  const value = process.env[name]
  if (!value) throw new Error(`${name} is not set; global-setup.ts sets it`)
  return value
}

const screenshots = join(__dirname, 'screenshots')

// waitForEnvelope resolves when the browser SDK has delivered an error event
// containing text to the monitor.
function waitForEnvelope(page: Page, text: string) {
  return page.waitForResponse(
    (r) => r.url().includes('/envelope/') && r.status() === 200 && (r.request().postData() ?? '').includes(text),
  )
}

test('Laravel and browser errors appear in the issue list and on the issue page', async ({ page, browser }) => {
  // Two visits to /boom and one to /boom-other in a real browser, so the
  // events carry a browser's User-Agent the way a visitor's would.
  const laravel = env('LARAVEL_URL')
  for (const path of ['/boom', '/boom', '/boom-other']) {
    const response = await page.goto(laravel + path)
    expect(response?.status(), `GET ${path}`).toBe(500)
  }

  // The browser example: a TypeError in a click handler and an unhandled rejection.
  await page.goto(`${env('BROWSER_EXAMPLE_URL')}/?dsn=${encodeURIComponent(env('SENTRY_DSN'))}`)
  await expect(page.getByText('Sentry is set up.')).toBeVisible()
  const typeErrorSent = waitForEnvelope(page, 'TypeError')
  await page.getByRole('button', { name: 'Throw a TypeError' }).click()
  await typeErrorSent
  const rejectionSent = waitForEnvelope(page, 'Failed to fetch /api/stock')
  await page.getByRole('button', { name: 'Reject a promise' }).click()
  await rejectionSent

  // The dashboard.
  // Signed in as the admin that global-setup.ts made on the setup page.
  const dashboard = await browser.newContext({ storageState: env('ADMIN_STATE') })
  const ui = await dashboard.newPage()
  // A controllable clock, so the test can jump to the page's next minute poll.
  await ui.clock.install()
  await ui.goto(`${env('MONITOR_URL')}/projects/${env('PROJECT_ID')}/issues`)

  // The writer stores events within a second; the page itself refreshes once
  // a minute, so reload until all four issues are listed.
  const rows = ui.locator('tbody tr')
  await expect(async () => {
    await ui.reload()
    await expect(rows).toHaveCount(4, { timeout: 1000 })
  }).toPass({ timeout: 20_000 })

  await expect(ui.getByRole('heading', { level: 1 })).toHaveText('4 open issues in Webshop')
  await expect(ui.getByText('4 of them are new in the last 24 hours.')).toBeVisible()

  // Two visits to /boom: one issue with two events, from Laravel.
  const runtime = rows.filter({ hasText: 'RuntimeException' })
  await expect(runtime).toHaveCount(1)
  await expect(runtime.locator('td').nth(2)).toHaveText('2')
  await expect(runtime).toContainText('Laravel')
  await expect(runtime).toContainText('Checkout failed')
  await expect(runtime).toContainText('CheckoutController.php:14')

  // /boom-other: a different exception, so an issue of its own.
  const other = rows.filter({ hasText: 'InvalidArgumentException' })
  await expect(other).toHaveCount(1)
  await expect(other.locator('td').nth(2)).toHaveText('1')
  await expect(other).toContainText('Laravel')

  // The browser SDK's errors, with source Browser and the file's path on the
  // site as culprit. An unhandled rejection is named that, as in the design.
  const typeError = rows.filter({ hasText: 'TypeError' })
  await expect(typeError).toHaveCount(1)
  await expect(typeError).toContainText("Cannot read properties of undefined (reading 'price')")
  await expect(typeError).toContainText('Browser')
  await expect(typeError).toContainText('src/main.js:21')
  const rejection = rows.filter({ hasText: 'Failed to fetch /api/stock' })
  await expect(rejection).toHaveCount(1)
  await expect(rejection.locator('strong')).toHaveText('Unhandled rejection')
  await expect(rejection).toContainText('Browser')
  await expect(rejection).toContainText('src/main.js:30')

  await ui.screenshot({ path: join(screenshots, 'phase1-issues.png'), fullPage: true })

  // The issue page: the controller frame open, with the line that threw marked.
  await runtime.getByRole('link').click()
  await expect(ui.getByRole('heading', { level: 1 })).toHaveText('RuntimeException')
  await expect(ui.getByRole('link', { name: 'Webshop issues' })).toBeVisible()
  const trace = ui.getByRole('region', { name: 'Stack trace' })
  await expect(trace).toContainText('RuntimeException: Checkout failed')
  await expect(trace).toContainText('app/Http/Controllers/CheckoutController.php')
  await expect(trace).toContainText('in store, line 14')
  const errorLine = trace.locator('[aria-current="true"]')
  await expect(errorLine).toHaveCount(1)
  await expect(errorLine).toContainText('14')
  await expect(errorLine).toContainText("throw new RuntimeException('Checkout failed');")
  await expect(trace.getByText(/framework lines? hidden/)).toBeVisible()
  await expect(ui.getByText('Users affected')).toBeVisible()
  await expect(ui.getByText('example@1.0.0')).toBeVisible()

  await ui.screenshot({ path: join(screenshots, 'phase1-issue-detail.png'), fullPage: true })

  // Framework code can be shown.
  await trace.getByRole('button', { name: 'Show framework code' }).click()
  await expect(trace.getByRole('button', { name: 'Hide framework code' })).toBeVisible()
  await expect(trace).toContainText('vendor/laravel/framework/')

  // A newer event of the same issue arrives while the page is open. The next
  // poll brings it (a new latest event) and must keep what the viewer opened.
  expect((await page.goto(laravel + '/boom'))?.status()).toBe(500)
  const events = ui.locator('dt:text-is("Events") + dd')
  await expect(async () => {
    const polled = ui.waitForResponse((r) => r.url().includes('/ui/api/issues/'))
    await ui.clock.fastForward(60_000)
    await polled
    await expect(events).toHaveText('3', { timeout: 1000 })
  }).toPass({ timeout: 20_000 })
  await expect(trace.getByRole('button', { name: 'Hide framework code' })).toBeVisible()
  await expect(trace).toContainText('vendor/laravel/framework/')

  await dashboard.close()
})
