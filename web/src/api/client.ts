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
  /** retryAfter is the Retry-After header in seconds, for a 429. */
  readonly retryAfter?: number

  constructor(status: number, message: string, retryAfter?: number) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.retryAfter = retryAfter
  }
}

/** The CSRF token of the session, from GET /ui/api/me or a sign-in answer; every change sends it back. */
let csrfToken = ''
/** The /ui/api/me request a change waits for when the token is not known yet. */
let csrfLoading: Promise<void> | undefined

/** setCsrfToken keeps the token of the signed-in session for the changes that follow. */
export function setCsrfToken(token: string) {
  csrfToken = token
}

/**
 * sessionCsrfToken returns the session's token. Right after the page loads
 * the shell's GET /ui/api/me may not have answered yet; then the change
 * asks for it itself (once, however many changes wait), instead of being
 * refused with 403.
 */
async function sessionCsrfToken(): Promise<string> {
  if (csrfToken) return csrfToken
  csrfLoading ??= getMe()
    .then((me) => setCsrfToken(me.csrfToken))
    .finally(() => {
      csrfLoading = undefined
    })
  await csrfLoading
  return csrfToken
}

/** onSignedOut is called when the API says the session is gone (it expired, or the user was removed). */
let onSignedOut: () => void = () => {}

export function setSignedOutHandler(handler: () => void) {
  onSignedOut = handler
}

async function getJSON<T>(path: string, signal?: AbortSignal): Promise<T> {
  return readJSON(await fetch(path, { signal, headers: { Accept: 'application/json' } }))
}

/** send makes a change: a JSON body, with the session's CSRF token. */
async function send<T>(method: 'POST' | 'PATCH' | 'PUT' | 'DELETE', path: string, body?: unknown): Promise<T> {
  const token = await sessionCsrfToken()
  const response = await fetch(path, {
    method,
    headers: { Accept: 'application/json', 'Content-Type': 'application/json', 'X-CSRF-Token': token },
    body: body === undefined ? undefined : JSON.stringify(body),
  })
  return readJSON(response)
}

/**
 * readJSON returns the body of a 2xx response, or throws an ApiError with
 * the API's message. A 401 means the session is gone, except for the
 * sign-in endpoints (signedIn false), which have none yet.
 */
async function readJSON<T>(response: Response, signedIn = true): Promise<T> {
  if (!response.ok) {
    let message = response.statusText
    try {
      const body = (await response.json()) as { error?: string }
      message = body.error ?? message
    } catch {
      // Not JSON; keep the status text.
    }
    const retry = Number(response.headers.get('Retry-After'))
    if (response.status === 401 && signedIn) onSignedOut()
    throw new ApiError(response.status, message, Number.isFinite(retry) && retry > 0 ? retry : undefined)
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
export function setIssueStatus(issueId: number, status: IssueStatus): Promise<{ id: number; projectId: number; status: IssueStatus }> {
  return send('POST', `/ui/api/issues/${issueId}/status`, { status })
}

export function getOverview(signal?: AbortSignal): Promise<Overview> {
  return getJSON('/ui/api/overview', signal)
}

// Accounts. The sign-in endpoints work without a session; they answer with
// the signed-in user (Me) and set the session cookie.

export type Role = 'admin' | 'member'

/** Me is the signed-in user. */
export interface Me {
  id: number
  email: string
  role: Role
  /** csrfToken goes along with every change; setCsrfToken keeps it. */
  csrfToken: string
  /** mailEnabled is true when the server mails invites and reset links (MAIL_URL is set). */
  mailEnabled: boolean
}

export interface AuthStatus {
  /** setupNeeded is true until the first account exists. */
  setupNeeded: boolean
  mailEnabled: boolean
}

export function getMe(signal?: AbortSignal): Promise<Me> {
  return getJSON('/ui/api/me', signal)
}

export function getAuthStatus(signal?: AbortSignal): Promise<AuthStatus> {
  return getJSON('/ui/api/auth/status', signal)
}

/**
 * signInRequest posts to a sign-in endpoint, which needs no session and no
 * CSRF token. The endpoints that sign in answer like /ui/api/me; their
 * token is kept at once, so a change made right after signing in has it.
 */
async function signInRequest<T>(path: string, body: unknown): Promise<T> {
  const response = await fetch(path, {
    method: 'POST',
    headers: { Accept: 'application/json', 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  })
  const answer = await readJSON<T>(response, false)
  const token = (answer as { csrfToken?: unknown } | null)?.csrfToken
  if (typeof token === 'string' && token) setCsrfToken(token)
  return answer
}

/** setup creates the first account, an admin. 404 once one exists. */
export function setup(email: string, password: string): Promise<Me> {
  return signInRequest('/ui/api/setup', { email, password })
}

/** login signs in. 401 for a wrong address or password, 429 after too many attempts. */
export function login(email: string, password: string): Promise<Me> {
  return signInRequest('/ui/api/login', { email, password })
}

export function logout(): Promise<object> {
  return send('POST', '/ui/api/logout', {})
}

export function changePassword(currentPassword: string, newPassword: string): Promise<object> {
  return send('POST', '/ui/api/me/password', { currentPassword, newPassword })
}

/** checkInvite reads an invite: 404 for an unknown link, 410 for one that expired or was used. */
export function checkInvite(token: string): Promise<{ email: string; role: Role }> {
  return signInRequest('/ui/api/invite/check', { token })
}

export function acceptInvite(token: string, password: string): Promise<Me> {
  return signInRequest('/ui/api/invite/accept', { token, password })
}

/** forgotPassword mails a reset link when the address has an account; it answers the same either way. */
export function forgotPassword(email: string): Promise<object> {
  return signInRequest('/ui/api/password/forgot', { email })
}

export function checkReset(token: string): Promise<{ email: string }> {
  return signInRequest('/ui/api/password/reset/check', { token })
}

export function resetPassword(token: string, password: string): Promise<Me> {
  return signInRequest('/ui/api/password/reset', { token, password })
}

// Settings, for admins.

/** SettingsProject is a row of "Your projects". */
export interface SettingsProject {
  id: number
  name: string
  createdAt: string
  /** null keeps data indefinitely. */
  retentionDays: number | null
  tracksLaravel: boolean
  tracksBrowser: boolean
  tracksPageviews: boolean
}

/** ProjectKeys is a project with what its Setup and keys page shows. */
export interface ProjectKeys extends SettingsProject {
  dsn: string
  posthogToken: string
  /** posthogHost is where the PostHog SDK sends to: this server. */
  posthogHost: string
}

export type ProjectSettings = Pick<SettingsProject, 'name' | 'retentionDays' | 'tracksLaravel' | 'tracksBrowser' | 'tracksPageviews'>

export function getSettingsProjects(signal?: AbortSignal): Promise<SettingsProject[]> {
  return getJSON('/ui/api/settings/projects', signal)
}

export function createProject(settings: ProjectSettings): Promise<ProjectKeys> {
  return send('POST', '/ui/api/settings/projects', settings)
}

export function getProjectKeys(projectId: number, signal?: AbortSignal): Promise<ProjectKeys> {
  return getJSON(`/ui/api/settings/projects/${projectId}`, signal)
}

/** updateProject changes the settings that are given; the others stay. */
export function updateProject(projectId: number, change: Partial<ProjectSettings>): Promise<ProjectKeys> {
  return send('PATCH', `/ui/api/settings/projects/${projectId}`, change)
}

/** rotateKeys gives the project a new DSN key and PostHog token; the old ones stop working at once. */
export function rotateKeys(projectId: number): Promise<ProjectKeys> {
  return send('POST', `/ui/api/settings/projects/${projectId}/rotate-keys`, {})
}

export function deleteProject(projectId: number): Promise<object> {
  return send('DELETE', `/ui/api/settings/projects/${projectId}`)
}

export interface SettingsUser {
  id: number
  email: string
  role: Role
  createdAt: string
  /** A member's projects; empty for admins, who see every project. */
  projectIds: number[]
}

export interface Invite {
  id: number
  email: string
  role: Role
  projectIds: number[]
  createdAt: string
  expiresAt: string
}

export interface UsersAndInvites {
  users: SettingsUser[]
  /** Invites that were neither used nor expired, newest first. */
  invites: Invite[]
}

/** A link to hand over. It is shown once; the server keeps only a hash of its token. */
export interface NewLink {
  link: string
  /** mailed is true when the server also mailed it. */
  mailed: boolean
}

export function getSettingsUsers(signal?: AbortSignal): Promise<UsersAndInvites> {
  return getJSON('/ui/api/settings/users', signal)
}

export function createInvite(email: string, role: Role, projectIds: number[]): Promise<NewLink & { invite: Invite }> {
  return send('POST', '/ui/api/settings/invites', { email, role, projectIds })
}

export function revokeInvite(inviteId: number): Promise<object> {
  return send('DELETE', `/ui/api/settings/invites/${inviteId}`)
}

/** updateUser changes a role, a member's projects, or both. 409 for the last admin. */
export function updateUser(userId: number, change: { role?: Role; projectIds?: number[] }): Promise<SettingsUser> {
  return send('PATCH', `/ui/api/settings/users/${userId}`, change)
}

export function deleteUser(userId: number): Promise<object> {
  return send('DELETE', `/ui/api/settings/users/${userId}`)
}

/** createResetLink makes a one-time link for a member to choose a new password, valid for an hour. */
export function createResetLink(userId: number): Promise<NewLink & { expiresAt: string }> {
  return send('POST', `/ui/api/settings/users/${userId}/reset-link`, {})
}

// Alerts of a project, for everyone who may see it.

export type DigestFrequency = 'daily' | 'weekly' | 'monthly'

/** AlertChannels says where one kind of alert goes. */
export interface AlertChannels {
  email: boolean
  slack: boolean
}

export interface AlertKinds {
  newIssue: AlertChannels
  regression: AlertChannels
  /** threshold: alert when the last hour has this many times the usual hourly errors (2 to 100). */
  spike: AlertChannels & { threshold: number }
  digest: AlertChannels & { frequency: DigestFrequency }
}

export interface ProjectAlerts {
  project: { id: number; name: string }
  /** mailEnabled is false without MAIL_URL: then nothing is mailed. */
  mailEnabled: boolean
  /** recipients is how many people get the project's mail: the admins and the members who see it. */
  recipients: number
  /** slackWebhook is the saved webhook, masked ("https://hooks.slack.com/…/abcd"); null when none. */
  slackWebhook: string | null
  alerts: AlertKinds
}

/** AlertsChange changes what is sent; slackWebhookUrl "" removes the webhook. */
export interface AlertsChange {
  alerts?: { [K in keyof AlertKinds]?: Partial<AlertKinds[K]> }
  slackWebhookUrl?: string
}

export function getProjectAlerts(projectId: number, signal?: AbortSignal): Promise<ProjectAlerts> {
  return getJSON(`/ui/api/projects/${projectId}/alerts`, signal)
}

export function updateProjectAlerts(projectId: number, change: AlertsChange): Promise<ProjectAlerts> {
  return send('PATCH', `/ui/api/projects/${projectId}/alerts`, change)
}

/** testProjectSlack posts a test message to webhookUrl, or to the saved webhook when it is left out. */
export function testProjectSlack(projectId: number, webhookUrl?: string): Promise<object> {
  return send('POST', `/ui/api/projects/${projectId}/alerts/slack-test`, webhookUrl ? { webhookUrl } : {})
}

// Settings → Status, for admins.

export interface ToolStatus {
  version: string
  startedAt: string
  uptimeSeconds: number
  /**
   * status is what /health says: ok; degraded (it works, but a check
   * failed, 200); error (it cannot work, 503). failing names the checks
   * that failed ("db", "disk").
   */
  status: 'ok' | 'degraded' | 'error'
  failing: string[]
  db: 'ok' | 'error'
  /** low: under 10 % or 1 GiB free; critical: under 1 % or 256 MiB; error: it cannot be read. */
  disk: 'ok' | 'low' | 'critical' | 'error'
  databaseBytes: number
  diskFreeBytes: number
  diskTotalBytes: number
  /** eventsLastHour counts the error events of the last hour, all projects. */
  eventsLastHour: number
  queueDepth: number
  queueCapacity: number
  /** lastBackup is the newest backup made before a database update. */
  lastBackup: { name: string; createdAt: string; sizeBytes: number } | null
  /** adminAlerts are the last alert of each kind about the tool, newest first. */
  adminAlerts: { kind: string; message: string; sentAt: string }[]
  /** adminSlackWebhook is masked; null when none is set. */
  adminSlackWebhook: string | null
  mailEnabled: boolean
  /** admins is how many people get the admin mails. */
  admins: number
}

export function getToolStatus(signal?: AbortSignal): Promise<ToolStatus> {
  return getJSON('/ui/api/settings/status', signal)
}

/** setAdminSlack saves the admins' webhook; "" removes it. */
export function setAdminSlack(webhookUrl: string): Promise<{ adminSlackWebhook: string | null }> {
  return send('PUT', '/ui/api/settings/admin-slack', { webhookUrl })
}

export function testAdminSlack(webhookUrl?: string): Promise<object> {
  return send('POST', '/ui/api/settings/admin-slack/test', webhookUrl ? { webhookUrl } : {})
}
