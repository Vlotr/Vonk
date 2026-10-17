// @vitest-environment happy-dom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { MemoryRouter, Route, Routes } from 'react-router'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'
import type { APIToken, Me, Project } from '../api/client'
import { MeContext } from '../layout/MeContext'
import { ProjectsContext } from '../layout/ProjectsContext'
import { SettingsApiTokensPage } from './SettingsApiTokensPage'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

const admin: Me = { id: 1, email: 'admin@example.com', role: 'admin', csrfToken: 't', mailEnabled: false }
const projects: Project[] = [
  { id: 1, name: 'Webshop', slug: 'webshop', sources: ['browser'], openIssues: 0, tracksPageviews: true, hasCustomEvents: false },
  { id: 2, name: 'Portal', slug: 'portal', sources: ['browser'], openIssues: 0, tracksPageviews: false, hasCustomEvents: false },
]
const deploys: APIToken = { id: 7, name: 'Deploys', hint: 'pHSI', projectIds: [1], createdAt: '2026-10-01T10:00:00Z', lastUsedAt: null }
const secret = 'sntrys_eyJ1cmwiOiJodHRwczovL21vbml0b3IuZXhhbXBsZS5jb20ifQ==_abcdefpHSI'

let root: Root
let container: HTMLElement
let tokens: APIToken[]
let posted: unknown[]
let deleted: string[]

beforeEach(() => {
  tokens = [deploys]
  posted = []
  deleted = []
  vi.stubGlobal(
    'fetch',
    vi.fn(async (url: string, init?: RequestInit) => {
      const json = (body: unknown, status = 200) =>
        new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
      if (url.endsWith('/ui/api/me')) return json(admin)
      if (init?.method === 'DELETE') {
        deleted.push(url)
        tokens = tokens.filter((t) => !url.endsWith(`/${t.id}`))
        return json({})
      }
      if (init?.method === 'POST') {
        const body = JSON.parse(String(init.body))
        posted.push(body)
        const made = { ...deploys, id: 8, name: body.name, projectIds: body.projectIds }
        tokens = [made, ...tokens]
        return json({ token: made, secret, url: 'https://monitor.example.com' }, 201)
      }
      return json({ tokens })
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
  for (let i = 0; i < 3; i++) {
    await act(async () => {
      await new Promise((r) => setTimeout(r, 0))
    })
  }
}

async function render(path = '/settings/api-tokens') {
  await act(async () => {
    root.render(
      <MeContext.Provider value={admin}>
        <ProjectsContext.Provider value={{ data: projects, stale: false, reload: () => {}, mutate: () => () => {} }}>
          <MemoryRouter initialEntries={[path]}>
            <Routes>
              <Route path="settings/api-tokens" element={<SettingsApiTokensPage />} />
            </Routes>
          </MemoryRouter>
        </ProjectsContext.Provider>
      </MeContext.Provider>,
    )
  })
  await settle()
}

const text = () => container.textContent ?? ''

test('tokens show by name, last four characters and projects, never whole', async () => {
  await render()
  expect(container.querySelector('h1')?.textContent).toBe('API tokens')
  expect(text()).toContain('Deploys')
  expect(text()).toContain('sntrys_…pHSI')
  expect(text()).toContain('Webshop')
  expect(text()).not.toContain('Portal')
  expect(text()).toContain('Never')
})

test('a new token is shown once, with the setup for the vite plugin and sentry-cli', async () => {
  await render('/settings/api-tokens?new')
  const input = container.querySelector<HTMLInputElement>('input[placeholder^="For example"]')
  const boxes = container.querySelectorAll<HTMLInputElement>('input[type="checkbox"]')
  await act(async () => {
    const setValue = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')?.set
    setValue?.call(input, 'CI')
    input?.dispatchEvent(new Event('input', { bubbles: true }))
    boxes[1].click() // Portal
  })
  await act(async () => {
    container.querySelector<HTMLButtonElement>('button[type="submit"]')?.click()
  })
  await settle()

  expect(posted).toEqual([{ name: 'CI', projectIds: [2] }])
  expect(text()).toContain('Token CI')
  expect(container.querySelector('[aria-label="Token"]')?.textContent).toBe(secret)
  expect(container.querySelector('[aria-label="Vite setup"]')?.textContent).toContain("project: 'portal'")
  expect(container.querySelector('[aria-label="Vite setup"]')?.textContent).toContain("url: 'https://monitor.example.com'")
  expect(container.querySelector('[aria-label="sentry-cli commands"]')?.textContent).toContain('--project portal')
  expect(text()).toContain('shown only this once')
})

test('revoking asks first, then removes the token', async () => {
  await render()
  const revokeButtons = () => Array.from(container.querySelectorAll('button')).filter((b) => b.textContent === 'Revoke')
  await act(async () => {
    revokeButtons()[0].click()
  })
  expect(text()).toContain('Uploads with Deploys stop working.')
  expect(deleted).toEqual([])

  // Cancel keeps it.
  await act(async () => {
    Array.from(container.querySelectorAll('button'))
      .find((b) => b.textContent === 'Cancel')
      ?.click()
  })
  expect(text()).not.toContain('stop working')
  expect(deleted).toEqual([])

  await act(async () => {
    revokeButtons()[0].click()
  })
  const confirm = container.querySelector<HTMLButtonElement>('button[class*="danger"]')
  expect(confirm?.textContent).toBe('Revoke')
  await act(async () => {
    confirm?.click()
  })
  await settle()
  expect(deleted).toEqual(['/ui/api/settings/api-tokens/7'])
  expect(text()).not.toContain('sntrys_…pHSI')
  expect(text()).toContain('No tokens yet')
})
