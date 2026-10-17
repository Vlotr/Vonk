// @vitest-environment happy-dom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { MemoryRouter, Route, Routes, useLocation } from 'react-router'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'
import type { EventList, EventSeries, Me } from '../api/client'
import { MeContext } from '../layout/MeContext'
import { EventsPage } from './EventsPage'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

const days = ['2026-10-10', '2026-10-11', '2026-10-12', '2026-10-13', '2026-10-14', '2026-10-15', '2026-10-16']

const week: EventList = {
  project: { id: 1, name: 'Webshop', tracksPageviews: true, hasCustomEvents: true },
  range: '7d',
  events: [
    { name: 'checkout_completed', count: 1_204, users: 980 },
    { name: 'product_viewed', count: 812, users: 401 },
  ],
  bots: 12,
}

function series(name: string, counts: number[]): EventSeries {
  return {
    name,
    range: '7d',
    count: counts.reduce((a, b) => a + b, 0),
    users: 3,
    // Days start at midnight in the browser's zone (the API is asked with tz), so the labels hold in every zone.
    series: days.map((d, i) => ({ start: new Date(`${d}T00:00:00`).toISOString(), count: counts[i] ?? 0 })),
  }
}

const admin: Me = { id: 1, email: 'admin@example.com', role: 'admin', csrfToken: 't', mailEnabled: false }

let root: Root
let container: HTMLElement
let list: EventList
let requested: string[]
let location = ''

beforeEach(() => {
  list = week
  requested = []
  vi.stubGlobal(
    'fetch',
    vi.fn(async (url: string) => {
      requested.push(url)
      const path = new URL(url, 'http://x').pathname
      const name = decodeURIComponent(path.split('/events/')[1] ?? '')
      const body = name ? series(name, name === 'checkout_completed' ? [170, 160, 180, 175, 165, 170, 184] : [100, 0, 0, 0, 0, 0, 712]) : list
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

function Where() {
  location = useLocation().search
  return null
}

async function render(path = '/projects/1/events') {
  await act(async () => {
    root.render(
      <MeContext.Provider value={admin}>
        <MemoryRouter initialEntries={[path]}>
          <Routes>
            <Route
              path="projects/:id/events"
              element={
                <>
                  <EventsPage />
                  <Where />
                </>
              }
            />
          </Routes>
        </MemoryRouter>
      </MeContext.Provider>,
    )
  })
  await settle()
}

const text = () => container.textContent ?? ''

test('the most frequent event first: a headline from the data, its chart and the list with counts and users', async () => {
  await render()
  expect(container.querySelector('h1')?.textContent).toBe('checkout_completed happened 1,204 times in the last 7\u00a0days')
  expect(text()).toContain('By 980 users. Webshop, last 7 days.')
  expect(text()).toContain('12 bot events not counted')
  expect(text()).toContain('Times per day')
  expect(text()).toContain('Oct 16')
  expect(text()).toContain('184')
  const rows = [...container.querySelectorAll('tbody tr')].map((tr) => [...tr.querySelectorAll('td')].map((td) => td.textContent))
  expect(rows).toEqual([
    ['checkout_completed', '1,204', '980'],
    ['product_viewed', '812', '401'],
  ])
  expect(container.querySelector('tbody tr[aria-current="true"]')?.textContent).toContain('checkout_completed')
  expect(requested[0]).toMatch(/^\/ui\/api\/projects\/1\/events\?range=7d/)
  expect(requested.some((u) => /\/events\/checkout_completed\?range=7d/.test(u))).toBe(true)
})

test('choosing another event shows its chart and keeps it in the URL', async () => {
  await render()
  const button = [...container.querySelectorAll('button')].find((b) => b.textContent === 'product_viewed')
  await act(async () => button?.click())
  await settle()
  expect(location).toBe('?event=product_viewed')
  expect(container.querySelector('h1')?.textContent).toBe('product_viewed happened 812 times in the last 7\u00a0days')
  expect(requested.at(-1)).toMatch(/\/events\/product_viewed\?range=7d/)
  expect(text()).toContain('712')
})

test('no custom events yet: what to do', async () => {
  list = { ...week, project: { ...week.project, hasCustomEvents: false }, events: [], bots: 0 }
  await render()
  expect(container.querySelector('h1')?.textContent).toBe('No custom events yet')
  expect(container.querySelector('table')).toBeNull()
  expect(text()).toContain("posthog.capture('checkout_completed')")
})

// A project without page views shows Events when it sends events from a
// server (posthog-node): the empty state names that way, not only posthog-js.
test('a project that sends events from a server, none in this period', async () => {
  list = { ...week, project: { ...week.project, tracksPageviews: false, hasCustomEvents: true }, events: [], bots: 0 }
  await render()
  expect(container.querySelector('h1')?.textContent).toBe('No custom events in the last 7\u00a0days')
  expect(text()).toContain('posthog-node')
  expect(text()).toContain('Setup and keys')
})
