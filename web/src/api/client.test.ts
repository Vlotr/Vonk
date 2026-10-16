import { afterEach, beforeEach, expect, test, vi } from 'vitest'

// Each test imports a fresh copy of the client, whose CSRF token starts empty
// as it does when the page has just loaded.
async function freshClient() {
  vi.resetModules()
  return import('./client')
}

interface Call {
  method: string
  path: string
  csrf: string | null
}

let calls: Call[]

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
}

beforeEach(() => {
  calls = []
  vi.stubGlobal(
    'fetch',
    vi.fn(async (path: string, init: RequestInit = {}) => {
      const headers = new Headers(init.headers)
      calls.push({ method: init.method ?? 'GET', path, csrf: headers.get('X-CSRF-Token') })
      if (path === '/ui/api/me') return json({ id: 1, email: 'a@example.com', role: 'admin', csrfToken: 'from-me', mailEnabled: false })
      if (path === '/ui/api/login') return json({ id: 1, email: 'a@example.com', role: 'admin', csrfToken: 'from-login', mailEnabled: false })
      return json({})
    }),
  )
})

afterEach(() => {
  vi.unstubAllGlobals()
})

test('a change right after the page loaded waits for the session token', async () => {
  const client = await freshClient()
  await client.updateProject(1, { tracksPageviews: true })
  expect(calls.map((c) => `${c.method} ${c.path}`)).toEqual(['GET /ui/api/me', 'PATCH /ui/api/settings/projects/1'])
  expect(calls[1].csrf).toBe('from-me')
})

test('two changes at once ask for the token once', async () => {
  const client = await freshClient()
  await Promise.all([client.updateProject(1, {}), client.updateProject(2, {})])
  expect(calls.filter((c) => c.path === '/ui/api/me')).toHaveLength(1)
  expect(calls.filter((c) => c.method === 'PATCH').map((c) => c.csrf)).toEqual(['from-me', 'from-me'])
})

test('signing in keeps the token of the new session', async () => {
  const client = await freshClient()
  await client.login('a@example.com', 'a long password')
  await client.updateProject(1, {})
  expect(calls.map((c) => `${c.method} ${c.path}`)).toEqual(['POST /ui/api/login', 'PATCH /ui/api/settings/projects/1'])
  expect(calls[1].csrf).toBe('from-login')
})
