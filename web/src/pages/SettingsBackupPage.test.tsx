// @vitest-environment happy-dom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { MemoryRouter } from 'react-router'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'
import type { BackupInfo, Me } from '../api/client'
import { MeContext } from '../layout/MeContext'
import { PG_DUMP, PG_RESTORE } from '../lib/backup'
import { SettingsBackupPage } from './SettingsBackupPage'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

const admin: Me = { id: 1, email: 'admin@example.com', role: 'admin', csrfToken: 'csrf-1', mailEnabled: false }
const sqlite: BackupInfo = {
  databaseKind: 'sqlite',
  databaseBytes: 3 * 2 ** 20,
  diskFreeBytes: 40 * 2 ** 30,
  canDownload: true,
  maxUploadBytes: 2 ** 30,
  backupsDir: '/data/backups',
  backups: [
    { name: 'pre-restore-0008-20261016T090000Z.db', kind: 'pre-restore', version: 8, createdAt: '2026-10-16T09:00:00Z', sizeBytes: 2 ** 20 },
    { name: 'pre-migrate-0007-20261015T080000Z.db', kind: 'pre-migrate', version: 7, createdAt: '2026-10-15T08:00:00Z', sizeBytes: 2048 },
  ],
  restorePending: null,
  restoreRejected: null,
}

let root: Root
let container: HTMLElement
let info: BackupInfo
let requests: { method: string; url: string; headers: Record<string, string>; body: unknown }[]
let answer: (method: string) => Response | undefined

const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

beforeEach(() => {
  info = sqlite
  requests = []
  answer = () => undefined
  vi.stubGlobal(
    'fetch',
    vi.fn(async (url: string, init?: RequestInit) => {
      const method = init?.method ?? 'GET'
      if (url.endsWith('/ui/api/me')) return json(admin) // the CSRF token, for the changes
      requests.push({ method, url, headers: (init?.headers ?? {}) as Record<string, string>, body: init?.body })
      const special = answer(method)
      if (special) return special
      if (method === 'POST') info = { ...info, restorePending: { sizeBytes: 4096, stagedAt: '2026-10-17T09:00:00Z' } }
      if (method === 'DELETE') info = { ...info, restorePending: null, restoreRejected: null }
      return json(info)
    }),
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

async function settle() {
  for (let i = 0; i < 4; i++) {
    await act(async () => {
      await new Promise((r) => setTimeout(r, 0))
    })
  }
}

async function render() {
  await act(async () => {
    root.render(
      <MeContext.Provider value={admin}>
        <MemoryRouter initialEntries={['/settings/backup']}>
          <SettingsBackupPage />
        </MemoryRouter>
      </MeContext.Provider>,
    )
  })
  await settle()
}

const card = (id: string) => container.querySelector(`[aria-labelledby="${id}"]`)?.textContent ?? ''

/** choose puts a file in the file field, as picking one does. */
async function choose(file: File) {
  const input = container.querySelector<HTMLInputElement>('input[type="file"]')
  if (!input) throw new Error('no file field')
  Object.defineProperty(input, 'files', { value: [file], configurable: true })
  await act(async () => {
    input.dispatchEvent(new Event('change', { bubbles: true }))
  })
}

async function submit() {
  await act(async () => {
    container.querySelector<HTMLFormElement>('form')?.requestSubmit()
  })
  await settle()
}

test('SQLite: a download link and the automatic backups with the command to restore one', async () => {
  await render()
  expect(container.querySelector('h1')?.textContent).toBe('Backup')
  const link = container.querySelector<HTMLAnchorElement>('a[download]')
  expect(link?.getAttribute('href')).toBe('/ui/api/backup')
  expect(link?.textContent).toBe('Download backup')
  expect(card('download-title')).toContain('3 MiB')
  const automatic = card('automatic-title')
  expect(automatic).toContain('Before a restore (version 8)')
  expect(automatic).toContain('Before a database update (version 7)')
  expect(automatic).toContain('/data/backups')
  expect(container.querySelector('[aria-label="Restore command"]')?.textContent).toBe(
    'monitor restore /data/backups/pre-restore-0008-20261016T090000Z.db',
  )
  expect(card('restore-title')).not.toContain('Restart required')
})

test('a backup is uploaded as it is, then waits: restart required, and it can be cancelled', async () => {
  await render()
  const file = new File(['SQLite format 3\u0000 and more'], 'monitor-backup.db')
  await choose(file)
  await submit()

  const post = requests.find((r) => r.method === 'POST')
  expect(post?.url).toBe('/ui/api/settings/backup/restore')
  expect(post?.headers['Content-Type']).toBe('application/octet-stream')
  expect(post?.headers['X-CSRF-Token']).toBe('csrf-1')
  expect(post?.body).toBe(file)
  expect(card('restore-title')).toContain('Restart required')
  expect(card('restore-title')).toContain('Restart the tool to restore it')

  await act(async () => {
    Array.from(container.querySelectorAll('button')).find((b) => b.textContent === 'Cancel the restore')?.click()
  })
  await settle()
  expect(requests.some((r) => r.method === 'DELETE' && r.url === '/ui/api/settings/backup/restore')).toBe(true)
  expect(card('restore-title')).not.toContain('Restart required')
})

test('a refused backup says why; a file too large is not sent at all', async () => {
  answer = (method) => (method === 'POST' ? json({ error: 'The file is damaged: SQLite cannot read it. Nothing was changed.' }, 400) : undefined)
  await render()
  await choose(new File(['broken'], 'broken.db'))
  await submit()
  expect(container.querySelector('[role="alert"]')?.textContent).toContain('The file is damaged')
  expect(card('restore-title')).not.toContain('Restart required')

  info = { ...sqlite, maxUploadBytes: 4 }
  requests = []
  await act(async () => root.unmount())
  root = createRoot(container)
  await render()
  await choose(new File(['more than four bytes'], 'big.db'))
  await submit()
  expect(container.querySelector('[role="alert"]')?.textContent).toContain('at most 4 B fits now')
  expect(requests.some((r) => r.method === 'POST')).toBe(false)
})

test('a restore the start refused is shown, with a way to remove the file', async () => {
  info = { ...sqlite, restoreRejected: { name: 'restore-rejected-20261017T080000Z.db', rejectedAt: '2026-10-17T08:00:00Z' } }
  await render()
  expect(card('restore-title')).toContain('The last restore was refused')
  expect(card('restore-title')).toContain('restore-rejected-20261017T080000Z.db')
})

test('without room on the disk there is no download link', async () => {
  info = { ...sqlite, canDownload: false, diskFreeBytes: 200 * 2 ** 20 }
  await render()
  expect(container.querySelector('a[download]')).toBeNull()
  expect(card('download-title')).toContain('not enough free disk space')
})

test('Postgres: pg_dump and pg_restore, no download and no upload', async () => {
  info = { ...sqlite, databaseKind: 'postgres', backups: [] }
  await render()
  expect(container.querySelector('[aria-label="Backup command"]')?.textContent).toBe(PG_DUMP)
  expect(container.querySelector('[aria-label="Restore command"]')?.textContent).toBe(PG_RESTORE)
  expect(container.querySelector('a[download]')).toBeNull()
  expect(container.querySelector('input[type="file"]')).toBeNull()
})
