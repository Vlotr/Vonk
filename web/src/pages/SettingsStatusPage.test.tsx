// @vitest-environment happy-dom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { MemoryRouter } from 'react-router'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'
import type { Me, ToolStatus } from '../api/client'
import { MeContext } from '../layout/MeContext'
import { PG_DUMP, SettingsStatusPage } from './SettingsStatusPage'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

const admin: Me = { id: 1, email: 'admin@example.com', role: 'admin', csrfToken: 't', mailEnabled: false }
const sqlite: ToolStatus = {
  version: '1.2.0',
  startedAt: '2026-10-16T10:00:00Z',
  uptimeSeconds: 3600,
  status: 'ok',
  failing: [],
  db: 'ok',
  disk: 'ok',
  databaseKind: 'sqlite',
  databaseBytes: 1_500_000,
  diskFreeBytes: 40 * 2 ** 30,
  diskTotalBytes: 100 * 2 ** 30,
  eventsLastHour: 12,
  queueDepth: 0,
  queueCapacity: 10_000,
  lastBackup: { name: 'pre-migrate-0007-20261015T080000Z.db', createdAt: '2026-10-15T08:00:00Z', sizeBytes: 2048 },
  adminAlerts: [],
  adminSlackWebhook: null,
  mailEnabled: false,
  admins: 1,
}

let root: Root
let container: HTMLElement
let status: ToolStatus

beforeEach(() => {
  status = sqlite
  vi.stubGlobal(
    'fetch',
    vi.fn(async () => new Response(JSON.stringify(status), { status: 200, headers: { 'Content-Type': 'application/json' } })),
  )
  container = document.createElement('div')
  document.body.append(container)
  root = createRoot(container)
})

afterEach(() => {
  act(() => root.unmount())
  container.remove()
  vi.unstubAllGlobals()
})

async function render() {
  await act(async () => {
    root.render(
      <MeContext.Provider value={admin}>
        <MemoryRouter initialEntries={['/settings/status']}>
          <SettingsStatusPage />
        </MemoryRouter>
      </MeContext.Provider>,
    )
  })
  for (let i = 0; i < 3; i++) {
    await act(async () => {
      await new Promise((r) => setTimeout(r, 0))
    })
  }
}

const backups = () => container.querySelector('[aria-labelledby="backup-title"]')?.textContent ?? ''

test('SQLite: the last backup made before an update', async () => {
  await render()
  expect(backups()).toContain('pre-migrate-0007-20261015T080000Z.db')
  expect(backups()).not.toContain('pg_dump')
})

test('Postgres: no backups by the tool, the pg_dump command instead', async () => {
  status = { ...sqlite, databaseKind: 'postgres', lastBackup: null }
  await render()
  expect(backups()).toContain('the tool makes no backups of it')
  expect(container.querySelector('[aria-label="Backup command"]')?.textContent).toBe(PG_DUMP)
  expect(backups()).not.toContain('No backup yet')
})
