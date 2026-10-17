import type { IssueSort, IssueSourceFilter, IssueStatus } from '../api/client'

// The issue list keeps its tab, filters and search in the URL query
// (?status=resolved&source=browser&sort=events&q=cart), so a reload, the
// back button or a shared link shows the same list.

export interface IssueFilters {
  status: IssueStatus
  /** '' means all sources. */
  source: IssueSourceFilter
  sort: IssueSort
  /** The search as typed; the API trims it. */
  q: string
  /** Only issues with an event on this route (linked from the Pages screen). */
  route: string
  /** Only issues with an event in this release (linked from an issue's Release). */
  release: string
}

export const DEFAULT_FILTERS: IssueFilters = { status: 'open', source: '', sort: 'last_seen', q: '', route: '', release: '' }

const STATUSES: readonly IssueStatus[] = ['open', 'resolved', 'ignored']
const SOURCES: readonly IssueSourceFilter[] = ['', 'laravel', 'browser']
const SORTS: readonly IssueSort[] = ['last_seen', 'first_seen', 'events', 'users']

function oneOf<T extends string>(value: string | null, allowed: readonly T[], fallback: T): T {
  return allowed.includes(value as T) ? (value as T) : fallback
}

/** parseIssueFilters reads the filters from a URL query; unknown values get the default. */
export function parseIssueFilters(params: URLSearchParams): IssueFilters {
  return {
    status: oneOf(params.get('status'), STATUSES, DEFAULT_FILTERS.status),
    source: oneOf(params.get('source'), SOURCES, DEFAULT_FILTERS.source),
    sort: oneOf(params.get('sort'), SORTS, DEFAULT_FILTERS.sort),
    q: params.get('q') ?? '',
    route: params.get('route') ?? '',
    release: params.get('release') ?? '',
  }
}

/**
 * filtersToParams writes the filters into a URL query, leaving defaults
 * out. Parameters of base that are not filters are kept.
 */
export function filtersToParams(filters: IssueFilters, base = new URLSearchParams()): URLSearchParams {
  const params = new URLSearchParams(base)
  for (const key of Object.keys(DEFAULT_FILTERS) as (keyof IssueFilters)[]) {
    if (filters[key] === DEFAULT_FILTERS[key]) params.delete(key)
    else params.set(key, filters[key])
  }
  return params
}

/** hasFilters tells whether the list is narrowed by source, search, route or release (the tab and order do not narrow it). */
export function hasFilters(filters: IssueFilters): boolean {
  return filters.source !== '' || filters.q.trim() !== '' || filters.route !== '' || filters.release !== ''
}
