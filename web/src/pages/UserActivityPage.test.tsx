// @vitest-environment happy-dom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { MemoryRouter, Route, Routes } from 'react-router'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'
import type { UserActivity } from '../api/client'
import { UserActivityPage } from './UserActivityPage'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

const minutesAgo = (m: number) => new Date(Date.now() - m * 60_000).toISOString()

const first: UserActivity = {
  project: { id: 1, name: 'Webshop' },
  userId: 'anna.de.vries@example.com',
  items: [
    { kind: 'error', at: minutesAgo(4), issueId: 7, type: 'TypeError', message: "Cannot read properties of undefined (reading 'price')", path: '/checkout', route: '/checkout' },
    { kind: 'event', at: minutesAgo(5), name: 'checkout_started', path: '/checkout', route: '/checkout', properties: { total: 49.95 } },
    { kind: 'pageview', at: minutesAgo(6), path: '/checkout', route: '/checkout' },
    { kind: 'pageview', at: minutesAgo(8), path: '/products/blue-shoes', route: '/products/:slug', durationMs: 100_000 },
  ],
  next: null,
}

// A full first page of 50 page views, and one more on the next page.
const full: UserActivity = {
  ...first,
  items: Array.from({ length: 50 }, (_, i) => ({ kind: 'pageview', at: minutesAgo(10 + i), path: `/p${i}`, route: `/p${i}` })),
  next: minutesAgo(59),
}

const older: UserActivity = {
  ...first,
  items: [{ kind: 'pageview', at: minutesAgo(70), path: '/products', route: '/products', durationMs: 2_000 }],
  next: null,
}

let root: Root
let container: HTMLElement
let requested: string[]
let firstPage: UserActivity

beforeEach(() => {
  requested = []
  firstPage = first
  vi.stubGlobal(
    'fetch',
    vi.fn(async (url: string) => {
      requested.push(url)
      const body = url.includes('before=') ? older : firstPage
      return new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } })
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

async function render(path = '/projects/1/users/anna.de.vries%40example.com') {
  await act(async () => {
    root.render(
      <MemoryRouter initialEntries={[path]}>
        <Routes>
          <Route path="projects/:id/users/:userId" element={<UserActivityPage />} />
        </Routes>
      </MemoryRouter>,
    )
  })
  await settle()
}

const text = () => container.textContent ?? ''

test("a user's page views, errors and custom events, the newest first", async () => {
  await render()
  expect(requested[0]).toBe('/ui/api/projects/1/users/anna.de.vries%40example.com/activity?limit=50')
  expect(container.querySelector('h1')?.textContent).toBe('User anna.de.vries@example.com was last seen 4 min ago')
  expect(text()).toContain('Today')
  const rows = [...container.querySelectorAll('li')].map((li) => li.textContent)
  expect(rows).toHaveLength(4)
  expect(rows[0]).toContain('TypeError')
  expect(rows[0]).toContain("Cannot read properties of undefined (reading 'price')")
  expect(rows[1]).toContain('checkout_started')
  expect(rows[1]).toContain('total: 49.95')
  expect(rows[3]).toContain('/products/blue-shoes')
  expect(rows[3]).toContain('1m 40s')
  // The error links to its issue.
  expect(container.querySelector('a[href="/projects/1/issues/7"]')?.textContent).toContain('TypeError')
})

test('Show older loads the next page under the first', async () => {
  firstPage = full
  await render()
  expect(container.querySelectorAll('li')).toHaveLength(50)
  const more = [...container.querySelectorAll('button')].find((b) => b.textContent === 'Show older')
  expect(more).toBeDefined()
  await act(async () => more?.click())
  await settle()
  expect(requested.at(-1)).toContain('before=')
  expect(container.querySelectorAll('li')).toHaveLength(51)
  expect(container.textContent).toContain('/products')
  expect([...container.querySelectorAll('button')].some((b) => b.textContent === 'Show older')).toBe(false)
})

test('a user id with nothing stored', async () => {
  firstPage = { ...first, items: [] }
  await render()
  expect(container.querySelector('h1')?.textContent).toBe('Nothing stored for user anna.de.vries@example.com')
  expect(container.querySelector('ol')).toBeNull()
})
