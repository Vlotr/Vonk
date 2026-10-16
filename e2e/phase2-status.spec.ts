// The end of phase 2: alerts and the tool's own status. The admin adds a
// Slack webhook on the Alerts page of Webshop (a fake Slack on this
// machine), sends a test message, and turns one alert off. A resolved issue
// that comes back through the real Laravel SDK then arrives in "Slack" as
// a returning issue. Settings → Status shows how the tool is doing.

import { expect, type Page, test } from '@playwright/test'
import { createServer, type Server } from 'node:http'
import type { AddressInfo } from 'node:net'
import { join } from 'node:path'

const env = (name: string) => {
  const value = process.env[name]
  if (!value) throw new Error(`${name} is not set; global-setup.ts sets it`)
  return value
}

const screenshots = join(__dirname, 'screenshots')

/** fakeSlack is an incoming webhook on 127.0.0.1 that keeps the texts posted to it. */
async function fakeSlack(): Promise<{ server: Server; url: string; texts: string[] }> {
  const texts: string[] = []
  const server = createServer((req, res) => {
    let body = ''
    req.on('data', (chunk) => (body += chunk))
    req.on('end', () => {
      texts.push((JSON.parse(body) as { text: string }).text)
      res.end('ok')
    })
  })
  await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', resolve))
  const { port } = server.address() as AddressInfo
  return { server, url: `http://127.0.0.1:${port}/services/T0E2E/B0E2E/e2e-secret-hook`, texts }
}

/** reloadUntil reloads the page until check passes: the dashboard itself polls only once a minute. */
async function reloadUntil(ui: Page, check: () => Promise<void>) {
  await expect(async () => {
    await ui.reload()
    await check()
  }).toPass({ timeout: 20_000 })
}

test('alerts go to Slack, and Settings shows the status of the tool', async ({ page, browser }) => {
  const monitor = env('MONITOR_URL')
  const slack = await fakeSlack()
  try {
    const ui = await (await browser.newContext({ storageState: env('ADMIN_STATE') })).newPage()

    // The sidebar leads to the project's alerts. Without MAIL_URL and a webhook, nothing goes out.
    await ui.goto(`${monitor}/projects/${env('PROJECT_ID')}/issues`)
    await ui.getByRole('navigation', { name: 'Webshop' }).getByRole('link', { name: 'Alerts' }).click()
    await expect(ui.getByRole('heading', { level: 1 })).toHaveText('Webshop sends no alerts')
    await expect(ui.getByText('Email is off. Set MAIL_URL to send email.')).toBeVisible()
    await expect(ui.getByLabel('New issue by email')).toBeDisabled()

    // A test message first, then the webhook is saved and shows only its end.
    await ui.getByLabel('Webhook URL').fill(slack.url)
    await ui.getByRole('button', { name: 'Send test message' }).click()
    await expect(ui.getByText('Test message sent. Check the channel.')).toBeVisible()
    expect(slack.texts).toEqual([`Test message from ${new URL(monitor).host}: alerts of Webshop will arrive here.`])
    await ui.getByRole('button', { name: 'Save webhook' }).click()
    await expect(ui.getByRole('heading', { level: 1 })).toHaveText('Webshop sends alerts to Slack')
    await expect(ui.locator('code', { hasText: '…/hook' })).toHaveText(/^http:\/\/127\.0\.0\.1:\d+\/…\/hook$/)
    await expect(ui.getByText('e2e-secret')).toHaveCount(0)

    // Choices are saved at once: spikes off for Slack, a daily summary.
    const saved = () => ui.waitForResponse((r) => r.url().endsWith('/alerts') && r.request().method() === 'PATCH' && r.ok())
    let save = saved()
    await ui.getByLabel('Spike to Slack').uncheck()
    await save
    save = saved()
    await ui.getByLabel('How often the summary comes').selectOption('daily')
    await save
    await ui.reload()
    await expect(ui.getByLabel('Spike to Slack')).not.toBeChecked()
    await expect(ui.getByLabel('Returning issue to Slack')).toBeChecked()
    await expect(ui.getByLabel('How often the summary comes')).toHaveValue('daily')
    await ui.screenshot({ path: join(screenshots, 'phase2-alerts.png'), fullPage: true })

    // A resolved issue comes back through sentry-laravel: Slack hears of it once.
    await ui.goto(`${monitor}/projects/${env('PROJECT_ID')}/issues`)
    const runtime = ui.locator('tbody tr').filter({ hasText: 'RuntimeException' })
    await reloadUntil(ui, () => expect(runtime).toHaveCount(1, { timeout: 1000 }))
    await runtime.getByRole('button', { name: 'Resolve' }).click()
    await expect(runtime).toHaveCount(0)
    expect((await page.goto(env('LARAVEL_URL') + '/boom'))?.status(), 'GET /boom').toBe(500)
    expect((await page.goto(env('LARAVEL_URL') + '/boom'))?.status(), 'GET /boom').toBe(500)
    await expect.poll(() => slack.texts.filter((t) => t.startsWith('*Returning issue in Webshop*')).length, { timeout: 15_000 }).toBe(1)
    const returning = slack.texts.find((t) => t.startsWith('*Returning issue in Webshop*')) ?? ''
    expect(returning).toMatch(/<http:\/\/127\.0\.0\.1:\d+\/projects\/\d+\/issues\/\d+\|RuntimeException: Checkout failed>/)
    expect(returning).toContain('`CheckoutController.php:')

    // Settings → Status, for admins.
    await ui.goto(`${monitor}/settings/projects`)
    await ui.getByRole('navigation', { name: 'Settings pages' }).getByRole('link', { name: 'Status' }).click()
    await expect(ui).toHaveURL(`${monitor}/settings/status`)
    await expect(ui.getByText(/^Version .+, running for .+\. The \/health check answers (OK|503 until this is fixed)\.$/)).toBeVisible()
    const server = ui.getByRole('region', { name: 'Server' })
    await expect(server).toContainText('Database')
    await expect(server).toContainText('Disk free')
    await expect(server).toContainText('of at most 10,000')
    await expect(ui.getByRole('region', { name: 'Backups' })).toContainText('No backup yet.')
    await expect(ui.getByRole('region', { name: 'Alerts for admins' })).toContainText('Email is off. Set MAIL_URL to send email.')
    await ui.screenshot({ path: join(screenshots, 'phase2-status.png'), fullPage: true })
  } finally {
    slack.server.close()
  }
})
