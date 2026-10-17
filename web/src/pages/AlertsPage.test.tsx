// @vitest-environment happy-dom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { MemoryRouter, Route, Routes } from 'react-router'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'
import type { Me, ProjectAlerts } from '../api/client'
import { MeContext } from '../layout/MeContext'
import { AlertsPage } from './AlertsPage'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

const alerts: ProjectAlerts = {
  project: { id: 1, name: 'Webshop' },
  mailEnabled: true,
  recipients: 2,
  slackWebhook: null,
  alerts: {
    newIssue: { email: true, slack: false },
    regression: { email: true, slack: false },
    spike: { email: false, slack: false, threshold: 5 },
    digest: { email: false, slack: false, frequency: 'weekly' },
  },
}

const admin: Me = { id: 1, email: 'admin@example.com', role: 'admin', csrfToken: 't', mailEnabled: true }
const member: Me = { ...admin, id: 2, email: 'member@example.com', role: 'member' }

let root: Root
let container: HTMLElement

beforeEach(() => {
  vi.stubGlobal(
    'fetch',
    vi.fn(async () => new Response(JSON.stringify(alerts), { status: 200, headers: { 'Content-Type': 'application/json' } })),
  )
  container = document.createElement('div')
  root = createRoot(container)
})

afterEach(() => {
  act(() => root.unmount())
  vi.unstubAllGlobals()
})

async function render(me: Me | undefined) {
  await act(async () => {
    root.render(
      <MeContext.Provider value={me}>
        <MemoryRouter initialEntries={['/projects/1/alerts']}>
          <Routes>
            <Route path="projects/:id/alerts" element={<AlertsPage />} />
          </Routes>
        </MemoryRouter>
      </MeContext.Provider>,
    )
  })
  // Let the alerts request answer.
  await act(async () => {
    await new Promise((r) => setTimeout(r, 0))
  })
}

const checkboxes = () => [...container.querySelectorAll<HTMLInputElement>('input[type="checkbox"]')]

test('until the signed-in user is known, the page shows neither the controls nor the read-only line', async () => {
  await render(undefined)
  expect(container.textContent).not.toContain('Only admins can change alerts.')
  expect(checkboxes()).toHaveLength(0)
  expect(container.querySelector('[aria-busy="true"]')).not.toBeNull()
})

test('an admin gets the controls at once, enabled', async () => {
  await render(admin)
  expect(container.textContent).not.toContain('Only admins can change alerts.')
  expect(checkboxes().length).toBeGreaterThan(0)
  expect(checkboxes().filter((c) => c.disabled)).toHaveLength(0)
})

test('a member sees the read-only page', async () => {
  await render(member)
  expect(container.textContent).toContain('Only admins can change alerts.')
  expect(checkboxes().every((c) => c.disabled)).toBe(true)
})
