// Typed calls to the dashboard API (/ui/api, served by internal/api in Go).
// Times arrive as RFC 3339 strings.

export type Source = 'laravel' | 'browser' | (string & {})

export type IssueStatus = 'open' | 'resolved' | 'ignored'

export interface Project {
  id: number
  name: string
  slug: string
  sources: Source[]
  openIssues: number
}

export interface Issue {
  id: number
  /** The exception type as sent, such as Illuminate\Database\QueryException; see shortType. */
  type: string
  message: string
  source: Source
  /** File and line of the top in-app frame, such as "CheckoutController.php:88". */
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
  counts: Record<IssueStatus, number>
  new24h: number
  total: number
  issues: Issue[]
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
  const response = await fetch(path, { signal, headers: { Accept: 'application/json' } })
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

export function getIssues(
  projectId: number,
  options: { status?: IssueStatus; limit?: number; offset?: number } = {},
  signal?: AbortSignal,
): Promise<IssueList> {
  const query = new URLSearchParams()
  if (options.status) query.set('status', options.status)
  if (options.limit) query.set('limit', String(options.limit))
  if (options.offset) query.set('offset', String(options.offset))
  const qs = query.toString()
  return getJSON(`/ui/api/projects/${projectId}/issues${qs ? `?${qs}` : ''}`, signal)
}

export function getIssue(issueId: number, signal?: AbortSignal): Promise<IssueDetail> {
  return getJSON(`/ui/api/issues/${issueId}`, signal)
}
