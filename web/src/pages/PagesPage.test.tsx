// @vitest-environment happy-dom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { MemoryRouter, Route, Routes } from 'react-router'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'
import type { Me, PageStats } from '../api/client'
import { MeContext } from '../layout/MeContext'
import { PagesPage } from './PagesPage'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

const days = ['2026-10-10', '2026-10-11', '2026-10-12', '2026-10-13', '2026-10-14', '2026-10-15', '2026-10-16']

const week: PageStats = {
  project: { id: 1, name: 'Webshop', tracksPageviews: true },
  range: '7d',
  totals: { views: 110_010, visitors: 21_340, avgTimeMs: 64_000 },
  series: days.map((d, i) => ({ start: `${d}T00:00:00+02:00`, views: [14_820, 15_310, 16_040, 15_620, 14_980, 15_820, 17_420][i] })),
  pages: [
    { route: '/', views: 32_410, visitors: 15_200, avgTimeMs: 38_000, errors: 0 },
    { route: '/products/:slug', views: 28_120, visitors: 11_030, avgTimeMs: 81_000, errors: 241 },
    { route: '/checkout', views: 6_920, visitors: 4_480, avgTimeMs: 134_000, errors: 1_204 },
    { route: '/robots-only', views: 0, visitors: 0, avgTimeMs: null, errors: 0 },
  ],
  bots: 312,
}

const admin: Me = { id: 1, email: 'admin@example.com', role: 'admin', csrfToken: 't', mailEnabled: false }

let root: Root
let container: HTMLElement
let answer: PageStats
let requested: string[]

beforeEach(() => {
  answer = week
  requested = []
  vi.stubGlobal(
    'fetch',
    vi.fn(async (url: string) => {
      requested.push(url)
      return new Response(JSON.stringify(answer), { status: 200, headers: { 'Content-Type': 'application/json' } })
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
  await act(async () => {
    await new Promise((r) => setTimeout(r, 0))
  })
}

async function render(path = '/projects/1/pages') {
  await act(async () => {
    root.render(
      <MeContext.Provider value={admin}>
        <MemoryRouter initialEntries={[path]}>
          <Routes>
            <Route path="projects/:id/pages" element={<PagesPage />} />
          </Routes>
        </MemoryRouter>
      </MeContext.Provider>,
    )
  })
  await settle()
}

const text = () => container.textContent ?? ''

test('shows the design: a headline from the data, totals, a bar per day and the pages by views', async () => {
  await render()
  expect(container.querySelector('h1')?.textContent).toBe('Visitors stay longest on /checkout')
  expect(text()).toContain('It is also the page with the most errors. Webshop, last 7 days.')
  expect(text()).toContain('110,010')
  expect(text()).toContain('21,340')
  expect(text()).toContain('1m 04s')
  expect(text()).toContain('312 bot visits not counted')
  expect(text()).toContain('Page views per day')
  expect(text()).toContain('Sorted by views')
  expect(text()).toContain('Oct 10')
  expect(text()).toContain('17,420')

  const rows = [...container.querySelectorAll('tbody tr')].map((tr) => tr.querySelector('td')?.textContent)
  expect(rows).toEqual(['/', '/products/:slug', '/checkout', '/robots-only'])
  // Errors link to the issue list of that page.
  const link = container.querySelector<HTMLAnchorElement>('a[href*="route="]')
  expect(link?.getAttribute('href')).toBe('/projects/1/issues?route=%2Fproducts%2F%3Aslug')
  expect(link?.textContent).toBe('241')
  // The page with the longest time fills its bar.
  expect(text()).toContain('2m 14s')
  expect(text()).toContain('No time yet')
  // The period is asked for, in the browser's time zone.
  expect(requested[0]).toMatch(/^\/ui\/api\/projects\/1\/pages\?range=7d(&tz=.+)?$/)
  expect(container.querySelector('[aria-pressed="true"]')?.textContent).toBe('7 days')
})

test('the toggle asks for another period', async () => {
  await render()
  answer = { ...week, range: '24h', series: week.series.slice(0, 1) }
  const day = [...container.querySelectorAll('button')].find((b) => b.textContent === '24 hours')
  await act(async () => day?.click())
  await settle()
  expect(requested.at(-1)).toContain('range=24h')
  expect(text()).toContain('Webshop, last 24 hours.')
  expect(text()).toContain('Page views per hour')
})

test('no page views yet: the setup steps are one click away', async () => {
  answer = { ...week, totals: { views: 0, visitors: 0, avgTimeMs: null }, series: [], pages: [], bots: 0 }
  await render()
  expect(container.querySelector('h1')?.textContent).toBe('No page views yet')
  expect(container.querySelector('table')).toBeNull()
  expect(container.querySelector('a[href="/settings/projects/1/setup#page-views"]')?.textContent).toBe('Setup and keys')
})
