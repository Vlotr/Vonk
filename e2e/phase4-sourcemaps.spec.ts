// Phase 4 ends with source maps: a browser error from a minified
// production build shows its original TypeScript file and line. An admin
// makes an API token in Settings → API tokens; the browser example is
// built for production with @sentry/vite-plugin, which uploads its source
// maps with that token to the monitor (the Sentry upload API); its
// TypeError then shows src/main.ts, showPrice and the TypeScript line on
// the issue page.

import { expect, test } from '@playwright/test'
import { type ChildProcess, spawn, spawnSync } from 'node:child_process'
import { existsSync, mkdtempSync, readdirSync, rmSync } from 'node:fs'
import { createServer } from 'node:net'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

const env = (name: string) => {
  const value = process.env[name]
  if (!value) throw new Error(`${name} is not set; global-setup.ts sets it`)
  return value
}

const screenshots = join(__dirname, 'screenshots')
const browserDir = resolve(__dirname, '../examples/browser')

/** freePort asks the system for a port nobody listens on. */
function freePort(): Promise<number> {
  return new Promise((resolvePort, reject) => {
    const server = createServer()
    server.on('error', reject)
    server.listen(0, '127.0.0.1', () => {
      const address = server.address()
      server.close(() => (typeof address === 'object' && address ? resolvePort(address.port) : reject(new Error('no port'))))
    })
  })
}

test('a minified browser error shows its TypeScript source once the build uploaded its maps', async ({ browser, page }) => {
  test.setTimeout(180_000)
  const monitor = env('MONITOR_URL')
  const projectId = env('PROJECT_ID')
  const release = 'browser-example@2.0.0'
  const admin = await (await browser.newContext({ storageState: env('ADMIN_STATE') })).newPage()

  // The token, made where admins make them: for Webshop only, shown once.
  await admin.goto(`${monitor}/settings/api-tokens`)
  await expect(admin.getByRole('heading', { level: 1 })).toHaveText('API tokens')
  await admin.getByRole('button', { name: 'New token' }).click()
  await admin.getByLabel('Name').fill('Browser example deploys')
  await admin.getByRole('checkbox', { name: 'Webshop' }).check()
  await admin.getByRole('button', { name: 'Make token' }).click()
  const shown = admin.getByLabel('Token', { exact: true })
  await expect(shown).toContainText('sntrys_')
  const token = (await shown.textContent())?.trim() ?? ''
  await expect(admin.getByLabel('Vite setup', { exact: true })).toContainText("project: 'webshop'")
  await admin.getByRole('button', { name: 'Done' }).click()
  const row = admin.locator('tbody tr').filter({ hasText: 'Browser example deploys' })
  await expect(row).toContainText(`sntrys_…${token.slice(-4)}`)
  await expect(row).toContainText('Never')

  // The production build: minified, its maps uploaded with the token and
  // then deleted from the build. The new Sentry CLI inside the plugin keeps
  // a cache; it goes in a temp directory, not in this machine's home.
  const cliHome = mkdtempSync(join(tmpdir(), 'monitor-e2e-sentry-'))
  rmSync(join(browserDir, 'dist'), { recursive: true, force: true })
  const build = spawnSync('npx', ['vite', 'build'], {
    cwd: browserDir,
    encoding: 'utf8',
    env: {
      ...process.env,
      SENTRY_AUTH_TOKEN: token,
      SENTRY_URL: monitor,
      SENTRY_PROJECT: 'webshop',
      VITE_SENTRY_RELEASE: release,
      SENTRY_CONFIG_DIR: cliHome,
      SENTRY_CLI_NO_TELEMETRY: '1',
    },
  })
  rmSync(cliHome, { recursive: true, force: true })
  expect(build.status, build.stderr).toBe(0)
  expect(build.stdout + build.stderr).toContain('Successfully uploaded source maps')
  const assets = readdirSync(join(browserDir, 'dist', 'assets'))
  expect(assets.some((f) => f.endsWith('.js'))).toBe(true)
  expect(assets.filter((f) => f.endsWith('.map'))).toEqual([]) // uploaded, not deployed

  // Serve the build as a site would, and click the button that fails.
  const port = await freePort()
  const site = `http://127.0.0.1:${port}`
  let preview: ChildProcess | undefined
  try {
    preview = spawn('npx', ['vite', 'preview', '--host', '127.0.0.1', '--port', String(port), '--strictPort'], {
      cwd: browserDir,
      detached: true,
      stdio: 'ignore',
    })
    await expect(async () => {
      expect((await fetch(site)).ok).toBe(true)
    }).toPass({ timeout: 20_000 })

    await page.goto(`${site}/?dsn=${encodeURIComponent(env('SENTRY_DSN'))}`)
    await expect(page.getByText('Sentry is set up.')).toBeVisible()
    const sent = page.waitForResponse(
      (r) => r.url().includes('/envelope/') && r.status() === 200 && (r.request().postData() ?? '').includes('TypeError'),
    )
    await page.getByRole('button', { name: 'Throw a TypeError' }).click()
    const envelope = (await sent).request().postData() ?? ''
    // What the browser sent is minified: a hashed bundle, no TypeScript.
    expect(envelope).toMatch(/assets\/index-[\w-]+\.js/)
    expect(envelope).not.toContain('main.ts')
  } finally {
    if (preview?.pid) {
      try {
        process.kill(-preview.pid, 'SIGTERM')
      } catch {
        // Already gone.
      }
    }
    rmSync(join(browserDir, 'dist'), { recursive: true, force: true })
  }

  // The issue, by its release: grouped and named by the original code.
  await admin.goto(`${monitor}/projects/${projectId}/issues?release=${encodeURIComponent(release)}`)
  const issue = admin.locator('tbody tr').filter({ hasText: 'TypeError' })
  await expect(async () => {
    await admin.reload()
    await expect(issue).toHaveCount(1, { timeout: 1000 })
  }).toPass({ timeout: 20_000 })
  await expect(issue).toContainText('src/main.ts:27')

  await issue.getByRole('link').click()
  await expect(admin.getByRole('heading', { level: 1 })).toHaveText('TypeError')
  const trace = admin.getByRole('region', { name: 'Stack trace' })
  await expect(trace).toContainText('src/main.ts')
  await expect(trace).toContainText('in showPrice, line 27')
  const errorLine = trace.locator('[aria-current="true"]')
  await expect(errorLine).toHaveCount(1)
  await expect(errorLine).toContainText('27')
  await expect(errorLine).toContainText('price.textContent = (product as Product).price.toFixed(2)')
  await expect(trace).not.toContainText('index-')
  await admin.screenshot({ path: join(screenshots, 'phase4-sourcemaps.png'), fullPage: true })

  // The token was used for the upload.
  await admin.goto(`${monitor}/settings/api-tokens`)
  await expect(admin.locator('tbody tr').filter({ hasText: 'Browser example deploys' })).not.toContainText('Never')
  await admin.screenshot({ path: join(screenshots, 'phase4-api-tokens.png'), fullPage: true })
  expect(existsSync(join(browserDir, 'dist'))).toBe(false)
})
