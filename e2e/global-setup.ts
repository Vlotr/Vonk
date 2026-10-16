// Starts everything the end-to-end tests need and returns the teardown:
//
//   1. builds bin/monitor with the dashboard (make build);
//   2. runs it on a free port with a temp DATA_DIR;
//   3. creates the first admin on the setup page, in Chromium, and keeps
//      that browser's session for the tests (ADMIN_STATE);
//   4. creates the projects "Webshop" and "Customer portal" with the CLI and
//      reads their DSNs;
//   5. serves the Laravel example (php artisan serve) with that DSN;
//   6. serves the browser example (Vite) on another free port;
//   7. serves the PostHog example (Vite) with Webshop's PostHog token and DSN.
//
// The tests read the addresses and the admin's session from the environment.
// Process output goes to e2e/.logs/ for when something fails.

import { chromium } from '@playwright/test'
import { type ChildProcess, spawn, spawnSync } from 'node:child_process'
import { randomBytes } from 'node:crypto'
import { createWriteStream, existsSync, mkdirSync, mkdtempSync, rmSync } from 'node:fs'
import { createServer } from 'node:net'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

const root = resolve(__dirname, '..')
const laravelDir = join(root, 'examples/laravel')
const browserDir = join(root, 'examples/browser')
const posthogDir = join(root, 'examples/posthog')
const logDir = join(__dirname, '.logs')

/** run runs a command to completion and fails setup when it fails. */
function run(command: string, args: string[], cwd: string) {
  const result = spawnSync(command, args, { cwd, stdio: 'inherit' })
  if (result.status !== 0) throw new Error(`${command} ${args.join(' ')} failed in ${cwd}`)
}

/** freePort asks the system for a port nobody listens on. */
function freePort(): Promise<number> {
  return new Promise((resolvePort, reject) => {
    const server = createServer()
    server.unref()
    server.on('error', reject)
    server.listen(0, '127.0.0.1', () => {
      const address = server.address()
      server.close(() => (typeof address === 'object' && address ? resolvePort(address.port) : reject(new Error('no port'))))
    })
  })
}

/** stopping holds the processes teardown is stopping, whose exit is expected. */
const stopping = new Set<ChildProcess>()

/** failures holds why a started process could not run or stopped early, by process. */
const failures = new Map<ChildProcess, string>()

/**
 * start runs a long-lived process in its own process group, so teardown can
 * stop it together with the children it starts (artisan serve runs php -S).
 * A process that cannot be started (no php or npx on the PATH) or that stops
 * early is noted in failures; waitFor then fails setup, which runs teardown
 * for everything started before it.
 */
function start(name: string, command: string, args: string[], cwd: string, env: NodeJS.ProcessEnv): ChildProcess {
  const log = createWriteStream(join(logDir, `${name}.log`))
  const child = spawn(command, args, { cwd, env, detached: true, stdio: ['ignore', 'pipe', 'pipe'] })
  child.stdout?.pipe(log)
  child.stderr?.pipe(log)
  child.on('error', (err) => {
    failures.set(child, `${name} could not be started (${command}: ${err.message})`)
    console.error(`[e2e] ${failures.get(child)}`)
  })
  child.on('exit', (code, signal) => {
    if (stopping.has(child)) return
    failures.set(child, `${name} stopped early (${signal ?? code}); see e2e/.logs/${name}.log`)
    console.error(`[e2e] ${failures.get(child)}`)
  })
  return child
}

function stop(child: ChildProcess) {
  if (child.pid === undefined || child.exitCode !== null) return
  stopping.add(child)
  try {
    process.kill(-child.pid, 'SIGTERM') // the whole group, which only holds what start() spawned
  } catch {
    // Already gone.
  }
}

/**
 * waitFor polls url until it answers at all (any status). It fails after
 * 30 s, or at once when child could not be started or stopped.
 */
async function waitFor(url: string, name: string, child: ChildProcess) {
  const deadline = Date.now() + 30_000
  while (Date.now() < deadline) {
    const failure = failures.get(child)
    if (failure) throw new Error(failure)
    try {
      await fetch(url)
      return
    } catch {
      await new Promise((r) => setTimeout(r, 200))
    }
  }
  throw new Error(`${name} did not start at ${url}; see e2e/.logs/`)
}

export default async function globalSetup() {
  rmSync(logDir, { recursive: true, force: true })
  mkdirSync(logDir, { recursive: true })
  const tmp = mkdtempSync(join(tmpdir(), 'monitor-e2e-'))
  const children: ChildProcess[] = []
  const teardown = async () => {
    children.forEach(stop)
    await new Promise((r) => setTimeout(r, 500))
    rmSync(tmp, { recursive: true, force: true })
  }

  try {
    run('make', ['build'], root)
    if (!existsSync(join(laravelDir, 'vendor'))) run('composer', ['install', '--no-interaction'], laravelDir)
    if (!existsSync(join(browserDir, 'node_modules'))) run('npm', ['ci'], browserDir)
    if (!existsSync(join(posthogDir, 'node_modules'))) run('npm', ['ci'], posthogDir)

    // The monitor, as an operator would run it.
    const port = await freePort()
    const monitorURL = `http://127.0.0.1:${port}`
    const monitorEnv = {
      PATH: process.env.PATH,
      SECRET_KEY: randomBytes(32).toString('hex'),
      BASE_URL: monitorURL,
      ADDR: `127.0.0.1:${port}`,
      DATA_DIR: join(tmp, 'data'),
      // The fake Slack of phase2-status.spec.ts speaks plain http; never set this in production.
      DEV_ALLOW_HTTP_WEBHOOKS: 'true',
    }
    const monitor = start('monitor', join(root, 'bin/monitor'), ['serve'], root, monitorEnv)
    children.push(monitor)
    await waitFor(`${monitorURL}/health`, 'monitor', monitor)

    // The first admin, made on the setup page as an operator would: every
    // page leads there until an account exists.
    const adminEmail = 'admin@example.com'
    const adminPassword = randomBytes(12).toString('hex')
    const adminState = join(tmp, 'admin-state.json')
    const browser = await chromium.launch()
    try {
      const setupPage = await browser.newPage()
      await setupPage.goto(monitorURL)
      await setupPage.waitForURL(`${monitorURL}/setup`)
      await setupPage.getByLabel('Email').fill(adminEmail)
      await setupPage.getByLabel('Password').fill(adminPassword)
      await setupPage.getByRole('button', { name: 'Create account' }).click()
      await setupPage.waitForURL(`${monitorURL}/`)
      await setupPage.context().storageState({ path: adminState })
    } finally {
      await browser.close()
    }

    const createProject = (name: string) => {
      const created = spawnSync(join(root, 'bin/monitor'), ['project', 'create', '--name', name], {
        env: monitorEnv,
        encoding: 'utf8',
      })
      const id = /^ID:\s+(\d+)$/m.exec(created.stdout)?.[1]
      const dsn = /^DSN:\s+(\S+)$/m.exec(created.stdout)?.[1]
      const token = /^PostHog token:\s+(\S+)$/m.exec(created.stdout)?.[1]
      if (created.status !== 0 || !id || !dsn || !token) throw new Error(`project create failed: ${created.stdout}${created.stderr}`)
      return { id, dsn, token }
    }
    const { id: projectId, dsn, token: posthogToken } = createProject('Webshop')
    // A second project for the All projects page (phase 2), fed by the browser example.
    const portal = createProject('Customer portal')

    // The Laravel example with the official sentry/sentry-laravel SDK. Real
    // environment variables win over a developer's .env; --no-reload passes
    // them on to the PHP server. File sessions and cache need no database.
    const laravelPort = await freePort()
    const laravelURL = `http://127.0.0.1:${laravelPort}`
    const laravel = start(
      'laravel',
      'php',
      ['artisan', 'serve', '--host=127.0.0.1', `--port=${laravelPort}`, '--no-reload'],
      laravelDir,
      {
        ...process.env,
        APP_KEY: `base64:${randomBytes(32).toString('base64')}`,
        APP_ENV: 'local',
        APP_DEBUG: 'true',
        SESSION_DRIVER: 'file',
        CACHE_STORE: 'file',
        QUEUE_CONNECTION: 'sync',
        LOG_CHANNEL: 'stderr',
        SENTRY_LARAVEL_DSN: dsn,
        SENTRY_RELEASE: 'example@1.0.0',
        // Screenshots are committed: show a neutral server, not this machine's name.
        SENTRY_SERVER_NAME: 'web-1',
      },
    )
    children.push(laravel)
    await waitFor(`${laravelURL}/up`, 'Laravel example', laravel)

    // The browser example with @sentry/browser, served by Vite.
    const browserPort = await freePort()
    const browserURL = `http://127.0.0.1:${browserPort}`
    const browserExample = start(
      'browser',
      'npx',
      ['vite', '--host', '127.0.0.1', '--port', String(browserPort), '--strictPort'],
      browserDir,
      process.env,
    )
    children.push(browserExample)
    await waitFor(browserURL, 'browser example', browserExample)

    // The PostHog example: posthog-js and @sentry/browser for Webshop, with
    // the setup the Setup and keys page gives (phase 3).
    const posthogPort = await freePort()
    const posthogURL = `http://127.0.0.1:${posthogPort}`
    const posthogExample = start(
      'posthog',
      'npx',
      ['vite', '--host', '127.0.0.1', '--port', String(posthogPort), '--strictPort'],
      posthogDir,
      { ...process.env, VITE_POSTHOG_TOKEN: posthogToken, VITE_POSTHOG_HOST: monitorURL, VITE_SENTRY_DSN: dsn },
    )
    children.push(posthogExample)
    await waitFor(posthogURL, 'PostHog example', posthogExample)

    Object.assign(process.env, {
      MONITOR_URL: monitorURL,
      ADMIN_STATE: adminState,
      ADMIN_EMAIL: adminEmail,
      ADMIN_PASSWORD: adminPassword,
      PROJECT_ID: projectId,
      SENTRY_DSN: dsn,
      PORTAL_PROJECT_ID: portal.id,
      PORTAL_DSN: portal.dsn,
      LARAVEL_URL: laravelURL,
      BROWSER_EXAMPLE_URL: browserURL,
      POSTHOG_EXAMPLE_URL: posthogURL,
    })
    return teardown
  } catch (err) {
    await teardown()
    throw err
  }
}
