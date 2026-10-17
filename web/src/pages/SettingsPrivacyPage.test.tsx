// @vitest-environment happy-dom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { MemoryRouter } from 'react-router'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'
import type { Me, Project, UserDataDeleted } from '../api/client'
import { MeContext } from '../layout/MeContext'
import { ProjectsContext } from '../layout/ProjectsContext'
import { SettingsPrivacyPage } from './SettingsPrivacyPage'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

const admin: Me = { id: 1, email: 'admin@example.com', role: 'admin', csrfToken: 't', mailEnabled: false }
const projects: Project[] = [
  { id: 1, name: 'Webshop', slug: 'webshop', sources: ['laravel'], openIssues: 0, tracksPageviews: true, hasCustomEvents: false },
  { id: 2, name: 'Portal', slug: 'portal', sources: ['browser'], openIssues: 0, tracksPageviews: false, hasCustomEvents: false },
]
const result: UserDataDeleted = {
  projects: 2,
  errorEvents: 3,
  pageviews: 2,
  pageleaves: 2,
  customEvents: 0,
  issuesDeleted: 1,
  issuesUpdated: 1,
  rows: 8,
}

let root: Root
let container: HTMLElement
let posted: unknown[]

beforeEach(() => {
  posted = []
  vi.stubGlobal(
    'fetch',
    vi.fn(async (url: string, init?: RequestInit) => {
      const json = (body: unknown, status = 200) =>
        new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
      if (url.endsWith('/ui/api/me')) return json(admin)
      posted.push(JSON.parse(String(init?.body)))
      return json(result)
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

async function render() {
  await act(async () => {
    root.render(
      <MeContext.Provider value={admin}>
        <ProjectsContext.Provider value={{ data: projects, stale: false, reload: () => {}, mutate: () => () => {} }}>
          <MemoryRouter initialEntries={['/settings/privacy']}>
            <SettingsPrivacyPage />
          </MemoryRouter>
        </ProjectsContext.Provider>
      </MeContext.Provider>,
    )
  })
  await settle()
}

const text = () => container.textContent ?? ''
const buttonNamed = (name: string) => Array.from(container.querySelectorAll('button')).find((b) => b.textContent === name)

async function typeUserId(value: string) {
  const input = container.querySelector<HTMLInputElement>('input[placeholder^="For example"]')
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')?.set?.call(input, value)
    input?.dispatchEvent(new Event('input', { bubbles: true }))
  })
}

async function click(el: HTMLElement | undefined | null) {
  await act(async () => {
    el?.click()
  })
  await settle()
}

test('it asks first, then deletes the user id in every project and says what went', async () => {
  await render()
  expect(container.querySelector('h1')?.textContent).toBe('Privacy')
  await typeUserId('  jan@example.com ')
  await click(buttonNamed('Delete data'))

  expect(posted).toEqual([])
  expect(container.querySelector('[role="alertdialog"]')?.textContent).toContain(
    'Delete everything stored with the user id “jan@example.com” in every project? This cannot be undone.',
  )
  await click(buttonNamed('Delete'))

  expect(posted).toEqual([{ userId: 'jan@example.com' }])
  expect(text()).toContain('Deleted 3 error events and 2 page views (with their 2 page leaves) of that user id in 2 projects.')
  expect(text()).toContain('1 issue had no other events and was removed; 1 issue was recounted.')
  expect(container.querySelector('[role="alertdialog"]')).toBeNull()
})

test('only the chosen projects; cancelling deletes nothing', async () => {
  await render()
  await typeUserId('42')
  await click(container.querySelectorAll<HTMLInputElement>('input[type="radio"]')[1]) // Only these projects
  await click(buttonNamed('Delete data'))
  expect(container.querySelector('[role="alert"]')?.textContent).toBe('Choose at least one project.')

  await click(container.querySelectorAll<HTMLInputElement>('input[type="checkbox"]')[1]) // Portal
  await click(buttonNamed('Delete data'))
  expect(container.querySelector('[role="alertdialog"]')?.textContent).toContain('the user id “42” in Portal?')
  await click(buttonNamed('Cancel'))
  expect(posted).toEqual([])

  await click(buttonNamed('Delete data'))
  await click(buttonNamed('Delete'))
  expect(posted).toEqual([{ userId: '42', projectIds: [2] }])
})

test('an empty user id is refused before anything is asked', async () => {
  await render()
  await typeUserId('   ')
  await click(buttonNamed('Delete data'))
  expect(container.querySelector('[role="alert"]')?.textContent).toBe('Give the user id whose data should be deleted.')
  expect(container.querySelector('[role="alertdialog"]')).toBeNull()
  expect(posted).toEqual([])
})
