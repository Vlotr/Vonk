// Phase 5, sharing the tool: an admin downloads a backup from Settings →
// Backup (a SQLite file), uploads it again to restore it, which waits for
// the next start ("Restart required") and is cancelled here; a damaged file
// is refused. Then Settings → Privacy deletes everything stored with one
// user id, after a second question: the person's errors and their issue
// are gone.

import { expect, test } from '@playwright/test'
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

const env = (name: string) => {
  const value = process.env[name]
  if (!value) throw new Error(`${name} is not set; global-setup.ts sets it`)
  return value
}

const screenshots = join(__dirname, 'screenshots')

/** sendError posts one Sentry error event of user to the project of dsn, as an SDK does. */
async function sendError(dsn: string, user: string, n: number) {
  const url = new URL(dsn)
  const projectId = url.pathname.replace(/\//g, '')
  const eventId = `${'0'.repeat(31)}${n}`
  const event = {
    event_id: eventId,
    platform: 'php',
    level: 'error',
    user: { id: user },
    exception: { values: [{ type: 'App\\Exceptions\\PrivacyCheckException', value: `Account of ${user} is locked` }] },
  }
  const body = `${JSON.stringify({ event_id: eventId })}\n${JSON.stringify({ type: 'event' })}\n${JSON.stringify(event)}\n`
  const res = await fetch(`${url.protocol}//${url.host}/api/${projectId}/envelope/`, {
    method: 'POST',
    headers: { 'X-Sentry-Auth': `Sentry sentry_version=7, sentry_key=${url.username}` },
    body,
  })
  expect(res.status, 'envelope').toBe(200)
}

test('a backup is downloaded and restored at the next start; a user id is deleted on request', async ({ browser }) => {
  const monitor = env('MONITOR_URL')
  const context = await browser.newContext({ storageState: env('ADMIN_STATE'), acceptDownloads: true })
  const ui = await context.newPage()

  await ui.goto(`${monitor}/settings/projects`)
  await ui.getByRole('navigation', { name: 'Settings pages' }).getByRole('link', { name: 'Backup' }).click()
  await expect(ui).toHaveURL(`${monitor}/settings/backup`)
  await expect(ui.getByRole('heading', { level: 1 })).toHaveText('Backup')

  if (process.env.E2E_DATABASE_URL) {
    // Postgres is backed up with pg_dump: no download, no upload.
    await expect(ui.getByLabel('Backup command')).toHaveText('pg_dump --format=custom --file=monitor.dump "$DATABASE_URL"')
    await expect(ui.getByRole('link', { name: 'Download backup' })).toHaveCount(0)
  } else {
    // The download is the whole database as a SQLite file.
    const downloading = ui.waitForEvent('download')
    await ui.getByRole('link', { name: 'Download backup' }).click()
    const download = await downloading
    expect(download.suggestedFilename()).toMatch(/^monitor-backup-\d{8}T\d{6}Z\.db$/)
    // The copies go in a folder of their own, removed at the end.
    const copies = mkdtempSync(join(tmpdir(), 'monitor-e2e-backup-'))
    try {
      const file = join(copies, download.suggestedFilename())
      await download.saveAs(file)
      const bytes = readFileSync(file)
      expect(bytes.subarray(0, 16).toString('latin1')).toBe('SQLite format 3\u0000')

      // A damaged file is refused and nothing waits.
      const damaged = `${file}.damaged.db`
      writeFileSync(damaged, bytes.subarray(0, Math.min(bytes.length, 8192)))
      await ui.getByLabel('Backup file').setInputFiles(damaged)
      await ui.getByRole('button', { name: 'Upload and check' }).click()
      await expect(ui.getByRole('alert')).toContainText('The file is damaged')
      await expect(ui.getByText('Restart required')).toHaveCount(0)

      // The download itself is accepted: it waits for the next start.
      await ui.getByLabel('Backup file').setInputFiles(file)
      await ui.getByRole('button', { name: 'Upload and check' }).click()
      await expect(ui.getByText('Restart required')).toBeVisible()
      await expect(ui.getByText(/is checked and waits for the next start/)).toBeVisible()
      // The backups folder is this run's temp directory; the committed
      // screenshot shows it as the container names it, so it names no machine.
      await ui
        .getByRole('region', { name: 'Automatic backups' })
        .locator('code')
        .evaluate((code) => {
          code.textContent = '/data/backups'
        })
      await ui.screenshot({ path: join(screenshots, 'phase5-backup.png'), fullPage: true })

      // Cancelled: the next start keeps the database (the other specs' data stays).
      await ui.getByRole('button', { name: 'Cancel the restore' }).click()
      await expect(ui.getByText('Restart required')).toHaveCount(0)
    } finally {
      rmSync(copies, { recursive: true, force: true })
    }
  }

  // Someone asks for their data to be deleted; they have errors in Webshop.
  const user = 'privacy-request@example.com'
  await sendError(env('SENTRY_DSN'), user, 1)
  await sendError(env('SENTRY_DSN'), user, 2)
  const issuesOf = async () => {
    const res = await context.request.get(`${monitor}/ui/api/projects/${env('PROJECT_ID')}/issues?q=PrivacyCheckException`)
    return ((await res.json()) as { total: number }).total
  }
  await expect.poll(issuesOf, { timeout: 10_000 }).toBe(1)

  await ui.getByRole('navigation', { name: 'Settings pages' }).getByRole('link', { name: 'Privacy' }).click()
  await expect(ui).toHaveURL(`${monitor}/settings/privacy`)
  await ui.getByLabel('User id').fill(user)
  await ui.getByRole('radio', { name: 'Only these projects' }).check()
  await ui.getByRole('checkbox', { name: 'Webshop' }).check()
  await ui.getByRole('button', { name: 'Delete data' }).click()
  const question = ui.getByRole('alertdialog')
  await expect(question).toContainText(`Delete everything stored with the user id “${user}” in Webshop? This cannot be undone.`)
  await question.getByRole('button', { name: 'Delete' }).click()
  const done = ui.getByRole('region', { name: 'Done' })
  await expect(done).toContainText('Deleted 2 error events of that user id in 1 project.')
  await expect(done).toContainText('1 issue had no other events and was removed.')
  await ui.screenshot({ path: join(screenshots, 'phase5-privacy.png'), fullPage: true })

  expect(await issuesOf()).toBe(0)
  const activity = await context.request.get(`${monitor}/ui/api/projects/${env('PROJECT_ID')}/users/${encodeURIComponent(user)}/activity`)
  expect(((await activity.json()) as { items: unknown[] }).items).toEqual([])
  await context.close()
})
