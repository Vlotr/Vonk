// @vitest-environment happy-dom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { MemoryRouter, Route, Routes } from 'react-router'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'
import type { Me, SettingsProject } from '../api/client'
import { MeContext } from '../layout/MeContext'
import { ProjectsContext } from '../layout/ProjectsContext'
import { SettingsProjectsPage } from './SettingsProjectsPage'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

const admin: Me = { id: 1, email: 'admin@example.com', role: 'admin', csrfToken: 't', mailEnabled: false }
const webshop: SettingsProject = {
  id: 1,
  name: 'Webshop',
  createdAt: '2026-10-01T10:00:00Z',
  retentionDays: 90,
  tracksLaravel: true,
  tracksBrowser: true,
  tracksPageviews: false,
  routePatterns: [],
  rateLimitPerMinute: 1000,
}

let root: Root
let container: HTMLElement
let patched: unknown[]

beforeEach(() => {
  patched = []
  vi.stubGlobal(
    'fetch',
    vi.fn(async (_url: string, init?: RequestInit) => {
      const json = (body: unknown, status = 200) =>
        new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
      if (init?.method === 'PATCH') {
        const body = JSON.parse(String(init.body))
        patched.push(body)
        return json({ ...webshop, ...body, dsn: '', posthogToken: '', posthogHost: '' })
      }
      return json([webshop])
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

async function render(path: string) {
  await act(async () => {
    root.render(
      <MeContext.Provider value={admin}>
        <ProjectsContext.Provider value={{ data: [], stale: false, reload: () => {}, mutate: () => () => {} }}>
          <MemoryRouter initialEntries={[path]}>
            <Routes>
              <Route path="settings/projects" element={<SettingsProjectsPage />} />
            </Routes>
          </MemoryRouter>
        </ProjectsContext.Provider>
      </MeContext.Provider>,
    )
  })
  await settle()
}

async function type(input: HTMLInputElement | null, value: string) {
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')?.set?.call(input, value)
    input?.dispatchEvent(new Event('input', { bubbles: true }))
  })
}

async function save() {
  await act(async () => {
    container.querySelector<HTMLButtonElement>('button[type="submit"]')?.click()
  })
  await settle()
}

const limitField = () => container.querySelector<HTMLInputElement>('input[aria-label="Events per minute"]')

test('Edit project changes the events-per-minute limit', async () => {
  await render('/settings/projects?edit=1')
  expect(limitField()?.value).toBe('1000')
  await type(limitField(), '250')
  await save()
  expect(patched).toHaveLength(1)
  expect(patched[0]).toMatchObject({ name: 'Webshop', rateLimitPerMinute: 250 })
})

test('a limit outside 10 to 100 000 is refused before it is sent', async () => {
  await render('/settings/projects?edit=1')
  await type(limitField(), '5')
  await save()
  expect(patched).toHaveLength(0)
  expect(container.querySelector('[role="alert"]')?.textContent).toBe('The events-per-minute limit is a whole number from 10 to 100 000.')
})

test('New project has no limit field: a new project starts at 1000', async () => {
  await render('/settings/projects?new')
  expect(limitField()).toBeNull()
})
