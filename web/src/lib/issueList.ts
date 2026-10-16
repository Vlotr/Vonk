import { getIssues, type Issue, type IssueList, type IssueStatus } from '../api/client'
import type { IssueFilters } from './issueFilters'

// The issue list of one project: loading everything "Show more" has opened,
// and the change a status button makes before the server confirms it.
// Pure functions, tested in issueList.test.ts.

/** The API sends at most this many issues per request. */
export const API_MAX_LIMIT = 100

/** LoadedIssues is a list with the tab it was loaded for, so rows from an older tab never get the new tab's buttons. */
export interface LoadedIssues extends IssueList {
  status: IssueStatus
}

/**
 * loadIssues loads the first `count` issues that match the filters, in
 * requests of at most 100, and joins them: the list a poll refreshes is
 * everything "Show more" has opened so far. get is the API call (tests
 * pass a fake).
 */
export async function loadIssues(
  projectId: number,
  filters: IssueFilters,
  count: number,
  signal: AbortSignal,
  get: typeof getIssues = getIssues,
): Promise<LoadedIssues> {
  const query = { status: filters.status, source: filters.source, sort: filters.sort, q: filters.q.trim(), route: filters.route }
  const first = await get(projectId, { ...query, limit: Math.min(count, API_MAX_LIMIT) }, signal)
  const rest = []
  for (let offset = API_MAX_LIMIT; offset < Math.min(count, first.total); offset += API_MAX_LIMIT) {
    rest.push(get(projectId, { ...query, limit: Math.min(API_MAX_LIMIT, count - offset), offset }, signal))
  }
  // An issue seen between two requests can move to the next page; show it once.
  const issues: Issue[] = []
  const seen = new Set<number>()
  for (const list of [first, ...(await Promise.all(rest))]) {
    for (const issue of list.issues) {
      if (seen.has(issue.id)) continue
      seen.add(issue.id)
      issues.push(issue)
    }
  }
  return { ...first, issues, status: filters.status }
}

/** withIssueMoved is the list after an issue moved from status `from` to `to`, before the server confirms it. */
export function withIssueMoved<T extends IssueList>(list: T, issue: Issue, from: IssueStatus, to: IssueStatus): T {
  const openChange = (to === 'open' ? 1 : 0) - (from === 'open' ? 1 : 0)
  return {
    ...list,
    issues: list.issues.filter((i) => i.id !== issue.id),
    counts: { ...list.counts, [from]: list.counts[from] - 1, [to]: list.counts[to] + 1 },
    total: list.total - 1,
    openIssues: list.openIssues + openChange,
    new24h: list.new24h + (issue.isNew ? openChange : 0),
  }
}
