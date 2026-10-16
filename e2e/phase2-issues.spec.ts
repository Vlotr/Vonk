// Phase 2 starts with the issue workflow: resolve an issue, see it come back
// when the same error happens again, and find the projects with new errors
// on the All projects page. The errors come from the real SDKs again: the
// Laravel example for Webshop, the browser example for Customer portal.

import { expect, type Locator, type Page, test } from '@playwright/test'
import { join } from 'node:path'

const env = (name: string) => {
  const value = process.env[name]
  if (!value) throw new Error(`${name} is not set; global-setup.ts sets it`)
  return value
}

const screenshots = join(__dirname, 'screenshots')

/** reloadUntil reloads the page until check passes: the dashboard itself polls only once a minute. */
async function reloadUntil(ui: Page, check: () => Promise<void>) {
  await expect(async () => {
    await ui.reload()
    await check()
  }).toPass({ timeout: 20_000 })
}

/** tab is the status tab whose label starts with name ("Open 3"). */
function tab(ui: Page, name: string): Locator {
  return ui.getByRole('group', { name: 'Status' }).getByRole('button', { name: new RegExp(`^${name} `) })
}

/** count reads the number in a status tab. */
async function count(tabButton: Locator): Promise<number> {
  return Number((await tabButton.innerText()).replace(/\D/g, ''))
}

test('a resolved issue comes back with the same error, and All projects shows the new errors', async ({ page, browser }) => {
  const laravel = env('LARAVEL_URL')
  expect((await page.goto(laravel + '/boom'))?.status(), 'GET /boom').toBe(500)

  // Customer portal gets an error from the browser example, so the overview
  // has two projects with new errors, as in the design.
  await page.goto(`${env('BROWSER_EXAMPLE_URL')}/?dsn=${encodeURIComponent(env('PORTAL_DSN'))}`)
  await expect(page.getByText('Sentry is set up.')).toBeVisible()
  const sent = page.waitForResponse((r) => r.url().includes('/envelope/') && r.status() === 200)
  await page.getByRole('button', { name: 'Throw a TypeError' }).click()
  await sent

  const dashboard = await browser.newContext({
    httpCredentials: { username: env('MONITOR_USER'), password: env('MONITOR_PASSWORD') },
  })
  const ui = await dashboard.newPage()
  const issuesURL = `${env('MONITOR_URL')}/projects/${env('PROJECT_ID')}/issues`
  await ui.goto(issuesURL)
  const runtime = ui.locator('tbody tr').filter({ hasText: 'RuntimeException' })
  await reloadUntil(ui, () => expect(runtime).toHaveCount(1, { timeout: 1000 }))

  // Resolve: the row leaves the Open list at once, the counts move, and the
  // sidebar's open count follows.
  const open = await count(tab(ui, 'Open'))
  const resolved = await count(tab(ui, 'Resolved'))
  await runtime.getByRole('button', { name: 'Resolve' }).click()
  await expect(runtime).toHaveCount(0)
  await expect(tab(ui, 'Open')).toHaveText(`Open ${open - 1}`)
  await expect(tab(ui, 'Resolved')).toHaveText(`Resolved ${resolved + 1}`)
  await expect(ui.getByRole('navigation', { name: 'Webshop' }).getByLabel(/open$/)).toHaveText(String(open - 1))

  // It is on the Resolved tab, which the URL remembers.
  await tab(ui, 'Resolved').click()
  await expect(ui).toHaveURL(/[?&]status=resolved\b/)
  await expect(runtime).toHaveCount(1)
  await expect(runtime.getByRole('button', { name: 'Reopen' })).toBeVisible()
  await ui.reload()
  await expect(tab(ui, 'Resolved')).toHaveAttribute('aria-pressed', 'true')
  await expect(runtime).toHaveCount(1)

  // Its page says so and offers Reopen.
  await runtime.getByRole('link').click()
  await expect(ui.getByRole('heading', { level: 1 })).toHaveText('RuntimeException')
  await expect(ui.getByText('Resolved', { exact: true })).toBeVisible()
  await expect(ui.getByRole('button', { name: 'Reopen' })).toBeVisible()
  await expect(ui.getByRole('button', { name: 'Copy link' })).toBeVisible()

  // The same error again: the issue is open again, and gone from Resolved.
  expect((await page.goto(laravel + '/boom'))?.status(), 'GET /boom').toBe(500)
  await reloadUntil(ui, () => expect(ui.getByRole('button', { name: 'Resolve' })).toBeVisible({ timeout: 1000 }))
  await ui.goto(issuesURL)
  await expect(runtime).toHaveCount(1)
  await expect(runtime.getByRole('button', { name: 'Resolve' })).toBeVisible()
  await expect(tab(ui, 'Open')).toHaveText(`Open ${open}`)
  await tab(ui, 'Resolved').click()
  await expect(runtime).toHaveCount(0)

  // All projects, from the sidebar: both projects have new errors, and the
  // newest issues across projects.
  await ui.getByRole('navigation', { name: 'Main' }).getByRole('link', { name: 'All projects' }).click()
  await expect(ui.getByRole('heading', { level: 1 })).toHaveText('2 projects have new errors in the last 24 hours')
  await expect(ui.getByText(/^Updated \d\d:\d\d\. New data comes in every minute\.$/)).toBeVisible()
  const projects = ui.getByRole('region', { name: 'Projects' }).locator('tbody tr')
  await expect(projects).toHaveCount(2)
  await expect(projects.filter({ hasText: 'Webshop' })).toContainText(/\d+ new/)
  await expect(projects.filter({ hasText: 'Customer portal' })).toContainText('1 new')
  await expect(projects.first()).toContainText('Add page tracking')
  const newest = ui.getByRole('region', { name: 'Newest issues' })
  await expect(newest.locator('tr').filter({ hasText: 'RuntimeException' })).toContainText('Webshop')
  await expect(newest.locator('tr').filter({ hasText: 'TypeError' }).filter({ hasText: 'Customer portal' })).toHaveCount(1)
  await ui.screenshot({ path: join(screenshots, 'phase2-all-projects.png'), fullPage: true })

  // A project row opens its issues.
  await projects.filter({ hasText: 'Customer portal' }).getByRole('link').click()
  await expect(ui.getByRole('heading', { level: 1 })).toHaveText('1 open issue in Customer portal')

  await dashboard.close()
})
