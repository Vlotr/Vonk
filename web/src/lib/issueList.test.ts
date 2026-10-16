import { expect, test } from 'vitest'
import type { Issue, IssueList, IssueQuery } from '../api/client'
import { DEFAULT_FILTERS } from './issueFilters'
import { loadIssues, withIssueMoved } from './issueList'

function issue(id: number, isNew = false): Issue {
  return {
    id,
    type: 'RuntimeException',
    message: `Issue ${id}`,
    source: 'laravel',
    culprit: 'a.php:1',
    events: 1,
    users: 0,
    firstSeen: '2026-10-16T12:00:00Z',
    lastSeen: '2026-10-16T12:00:00Z',
    isNew,
    hourly: [],
  }
}

function list(issues: Issue[], over: Partial<IssueList> = {}): IssueList {
  return {
    project: { id: 1, name: 'Webshop' },
    counts: { open: 14, resolved: 128, ignored: 9 },
    openIssues: 14,
    new24h: 6,
    total: 14,
    issues,
    ...over,
  }
}

test('resolving a new open issue moves it out of the open tab and out of the headline numbers', () => {
  const moved = withIssueMoved(list([issue(1, true), issue(2)]), issue(1, true), 'open', 'resolved')
  expect(moved.issues.map((i) => i.id)).toEqual([2])
  expect(moved.counts).toEqual({ open: 13, resolved: 129, ignored: 9 })
  expect(moved.total).toBe(13)
  expect(moved.openIssues).toBe(13)
  expect(moved.new24h).toBe(5)
})

test('resolving an older open issue leaves the new count alone', () => {
  const moved = withIssueMoved(list([issue(2)]), issue(2), 'open', 'resolved')
  expect(moved.openIssues).toBe(13)
  expect(moved.new24h).toBe(6)
})

test('reopening adds the issue back to the open numbers', () => {
  const resolvedTab = list([issue(3, true)], { total: 128 })
  const moved = withIssueMoved(resolvedTab, issue(3, true), 'resolved', 'open')
  expect(moved.issues).toEqual([])
  expect(moved.counts).toEqual({ open: 15, resolved: 127, ignored: 9 })
  expect(moved.total).toBe(127)
  expect(moved.openIssues).toBe(15)
  expect(moved.new24h).toBe(7)
})

test('ignoring a resolved issue changes neither open number', () => {
  const moved = withIssueMoved(list([issue(4, true)], { total: 128 }), issue(4, true), 'resolved', 'ignored')
  expect(moved.counts).toEqual({ open: 14, resolved: 127, ignored: 10 })
  expect(moved.openIssues).toBe(14)
  expect(moved.new24h).toBe(6)
})

/** A fake API with `total` issues; it records every request. */
function fakeAPI(total: number, shiftAfterFirst = 0) {
  const requests: IssueQuery[] = []
  const all = Array.from({ length: total + shiftAfterFirst }, (_, i) => issue(i + 1))
  const get = async (_projectId: number, query: IssueQuery = {}) => {
    requests.push(query)
    // After the first request, `shiftAfterFirst` new issues arrive at the top,
    // pushing the rows down: the next page repeats some of the first page.
    const rows = requests.length === 1 ? all.slice(shiftAfterFirst) : all
    const offset = query.offset ?? 0
    return list(rows.slice(offset, offset + (query.limit ?? 25)), { total })
  }
  return { get, requests }
}

test('loadIssues asks for at most 100 issues per request', async () => {
  const api = fakeAPI(260)
  const loaded = await loadIssues(1, DEFAULT_FILTERS, 250, new AbortController().signal, api.get)
  expect(api.requests.map((r) => [r.offset ?? 0, r.limit])).toEqual([
    [0, 100],
    [100, 100],
    [200, 50],
  ])
  expect(loaded.issues).toHaveLength(250)
  expect(loaded.status).toBe('open')
})

test('loadIssues stops at the total', async () => {
  const api = fakeAPI(130)
  const loaded = await loadIssues(1, DEFAULT_FILTERS, 300, new AbortController().signal, api.get)
  expect(api.requests.map((r) => [r.offset ?? 0, r.limit])).toEqual([
    [0, 100],
    [100, 100],
  ])
  expect(loaded.issues).toHaveLength(130)
})

test('loadIssues shows an issue that moved to the next page between requests once', async () => {
  const api = fakeAPI(150, 2)
  const loaded = await loadIssues(1, DEFAULT_FILTERS, 150, new AbortController().signal, api.get)
  const ids = loaded.issues.map((i) => i.id)
  expect(new Set(ids).size).toBe(ids.length)
})

test('loadIssues sends the filters with the search trimmed', async () => {
  const api = fakeAPI(1)
  await loadIssues(1, { ...DEFAULT_FILTERS, status: 'resolved', q: '  cart ', sort: 'events' }, 25, new AbortController().signal, api.get)
  expect(api.requests[0]).toMatchObject({ status: 'resolved', q: 'cart', sort: 'events', limit: 25 })
})
