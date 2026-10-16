// Typed calls to the dashboard API (/ui/api, served by internal/api in Go).
// Times arrive as RFC 3339 strings.

export type Source = 'laravel' | 'browser' | (string & {})

export type IssueStatus = 'open' | 'resolved' | 'ignored'

/** The orders of the issue list: most recently seen (the default), newest, most events, most users. */
export type IssueSort = 'last_seen' | 'first_seen' | 'events' | 'users'

/** The sources the issue list filters on; '' for all. */
export type IssueSourceFilter = '' | 'laravel' | 'browser'

export interface Project {
  id: number
  name: string
  slug: string
  sources: Source[]
  openIssues: number
}

export interface Issue {
  id: number
  /**
   * The exception type as sent, such as Illuminate\Database\QueryException (see
   * shortType), or "Unhandled rejection" for an unhandled promise rejection.
   */
  type: string
  message: string
  source: Source
  /** File and line of the top in-app frame: "CheckoutController.php:88", or for browsers the path "assets/cart.js:142". */
  culprit: string
  events: number
  users: number
  firstSeen: string
  lastSeen: string
  isNew: boolean
  /** Events per clock hour over the last 24 hours, oldest first. */
  hourly: number[]
}

export interface IssueList {
  project: { id: number; name: string }
  /** Issues per status that match the filters (source, q, route): the status tabs. */
  counts: Record<IssueStatus, number>
  /** The project's open issues, ignoring the filters: the headline. */
  openIssues: number
  /** The project's open issues first seen in the last 24 hours, ignoring the filters. */
  new24h: number
  /** Issues with the requested status that match the filters, for paging. */
  total: number
  issues: Issue[]
}

export interface IssueQuery {
  status?: IssueStatus
  source?: IssueSourceFilter
  sort?: IssueSort
  /** Part of the title, message, culprit or an event's URL. */
  q?: string
  route?: string
  /** 1 to 100. */
  limit?: number
  offset?: number
}

/** OverviewProject is a row of the All projects table. */
export interface OverviewProject {
  id: number
  name: string
  sources: Source[]
  /** Open issues first seen in the last 24 hours. */
  newIssues24h: number
  /** Error events in the last 24 hours. */
  errors24h: number
  /** Error events per clock hour, oldest first; the last entry is the current hour. */
  hourly: number[]
  /** Null until page tracking exists. */
  pageviews24h: number | null
  avgTimeOnPageMs: number | null
}

export interface NewestIssue {
  id: number
  projectId: number
  projectName: string
  type: string
  message: string
  lastSeen: string
}

export interface Overview {
  updatedAt: string
  /** New issues first, then the most errors, then by name. */
  projects: OverviewProject[]
  /** Open issues first seen in the last 24 hours, across projects, the most recently seen first. */
  newestIssues: NewestIssue[]
}

export interface Frame {
  file: string
  function: string
  line: number
  inApp: boolean
  preContext: string[]
  contextLine: string
  postContext: string[]
}

/** The newest event of an issue. Optional fields are missing when the event did not carry them. */
export interface EventDetail {
  eventId: string
  timestamp: string
  exception?: { type: string; value: string }
  /** The most recent call first. */
  frames: Frame[]
  user?: { id: string }
  browser?: string
  os?: string
  environment?: string
  server?: string
  runtime?: { name: string; version: string }
  url?: string
  release?: string
  /** ISO 3166 country code. */
  country?: string
}

export interface IssueDetail {
  issue: Issue & { status: IssueStatus; projectId: number }
  hourly: number[]
  latestEvent: EventDetail | null
}

/** ApiError is a response that was not 2xx; status 404 means "not found or not yours". */
export class ApiError extends Error {
  readonly status: number

  constructor(status: number, message: string) {
    super(message)
    this.name = 'ApiError'
    this.status = status
  }
}

async function getJSON<T>(path: string, signal?: AbortSignal): Promise<T> {
  return readJSON(await fetch(path, { signal, headers: { Accept: 'application/json' } }))
}

/** readJSON returns the body of a 2xx response, or throws an ApiError with the API's message. */
async function readJSON<T>(response: Response): Promise<T> {
  if (!response.ok) {
    let message = response.statusText
    try {
      const body = (await response.json()) as { error?: string }
      message = body.error ?? message
    } catch {
      // Not JSON; keep the status text.
    }
    throw new ApiError(response.status, message)
  }
  return (await response.json()) as T
}

export function getProjects(signal?: AbortSignal): Promise<Project[]> {
  return getJSON('/ui/api/projects', signal)
}

export function getIssues(projectId: number, options: IssueQuery = {}, signal?: AbortSignal): Promise<IssueList> {
  const query = new URLSearchParams()
  for (const [key, value] of Object.entries(options)) {
    if (value !== undefined && value !== '' && value !== 0) query.set(key, String(value))
  }
  const qs = query.toString()
  return getJSON(`/ui/api/projects/${projectId}/issues${qs ? `?${qs}` : ''}`, signal)
}

export function getIssue(issueId: number, signal?: AbortSignal): Promise<IssueDetail> {
  return getJSON(`/ui/api/issues/${issueId}`, signal)
}

/** setIssueStatus opens, resolves or ignores an issue. */
export async function setIssueStatus(
  issueId: number,
  status: IssueStatus,
): Promise<{ id: number; projectId: number; status: IssueStatus }> {
  const response = await fetch(`/ui/api/issues/${issueId}/status`, {
    method: 'POST',
    headers: { Accept: 'application/json', 'Content-Type': 'application/json' },
    body: JSON.stringify({ status }),
  })
  return readJSON(response)
}

export function getOverview(signal?: AbortSignal): Promise<Overview> {
  return getJSON('/ui/api/overview', signal)
}
