// Text the dashboard shows: names, headlines, times. Pure functions, tested in format.test.ts.

/**
 * shortType shows an exception type the way the design does: the part after
 * the last "\" or "." (QueryException for Illuminate\Database\QueryException).
 * The API keeps the full name; the issue page shows it in the stack trace.
 */
export function shortType(type: string): string {
  if (/\s/.test(type)) return type // a message used as a title, not a class name
  const short = type.split(/[\\.]/).pop() ?? ''
  return short === '' ? type : short
}

/** plural writes "1 issue", "2 issues". */
export function plural(n: number, word: string): string {
  return `${n} ${word}${n === 1 ? '' : 's'}`
}

/** issuesHeadline is the issue list's headline: "14 open issues in Webshop". */
export function issuesHeadline(open: number, projectName: string): string {
  if (open === 0) return `No open issues in ${projectName}`
  return `${plural(open, 'open issue')} in ${projectName}`
}

/** newIssuesSentence says how many of the open issues appeared in the last 24 hours. */
export function newIssuesSentence(open: number, newCount: number): string {
  if (open === 0) return 'New errors show up here within a minute after your app sends them.'
  if (open === 1) return newCount > 0 ? 'It is new in the last 24 hours.' : 'It was first seen more than 24 hours ago.'
  if (newCount === 0) return 'None of them are new in the last 24 hours.'
  return `${newCount} of them ${newCount === 1 ? 'is' : 'are'} new in the last 24 hours.`
}

/** showingSentence is the footer of the issue list: "Showing 6 of 14 open issues". */
export function showingSentence(shown: number, total: number, status: string): string {
  return `Showing ${shown} of ${plural(total, `${status} issue`)}`
}

/**
 * emptyIssuesSentence is what the issue list says when a tab has no rows:
 * "No resolved issues.", or with filters "No open issues match these
 * filters." and with a search "No open issues match “cart.js”."
 */
export function emptyIssuesSentence(status: string, filtered: boolean, search: string): string {
  if (search) return `No ${status} issues match \u201c${search}\u201d.`
  if (filtered) return `No ${status} issues match these filters.`
  return status === 'open' ? 'No open issues right now.' : `No ${status} issues.`
}

/** overviewHeadline is the All projects headline: "2 projects have new errors in the last 24 hours". */
export function overviewHeadline(projects: { newIssues24h: number }[]): string {
  if (projects.length === 0) return 'No projects yet'
  const withNew = projects.filter((p) => p.newIssues24h > 0).length
  if (withNew === 0) return 'No new errors in the last 24 hours'
  return `${plural(withNew, 'project')} ${withNew === 1 ? 'has' : 'have'} new errors in the last 24 hours`
}

const MONTHS = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec']

function twoDigits(n: number): string {
  return String(n).padStart(2, '0')
}

/** formatDate writes "Sep 6", with the year when it is not this year: "Jan 2, 2025". */
export function formatDate(date: Date, now: Date): string {
  const day = `${MONTHS[date.getMonth()]} ${date.getDate()}`
  return date.getFullYear() === now.getFullYear() ? day : `${day}, ${date.getFullYear()}`
}

/** formatTime writes "10:42": local 24-hour time. */
export function formatTime(date: Date): string {
  return `${twoDigits(date.getHours())}:${twoDigits(date.getMinutes())}`
}

/**
 * formatDateTime writes "Oct 15, 21:06" in local time, with the year when it
 * is not this year: "Dec 31, 2025, 23:59" (the date as formatDate writes it).
 */
export function formatDateTime(date: Date, now: Date): string {
  return `${formatDate(date, now)}, ${formatTime(date)}`
}

/** formatDuration writes a time on a page: "1m 12s", "0m 48s". */
export function formatDuration(ms: number): string {
  const seconds = Math.round(ms / 1000)
  return `${Math.floor(seconds / 60)}m ${twoDigits(seconds % 60)}s`
}

/** timeAgo writes "4 min ago", "3 h ago", "2 d ago", and a date after a month. */
export function timeAgo(date: Date, now: Date): string {
  const seconds = Math.floor((now.getTime() - date.getTime()) / 1000)
  if (seconds < 60) return 'just now'
  const minutes = Math.floor(seconds / 60)
  if (minutes < 60) return `${minutes} min ago`
  const hours = Math.floor(minutes / 60)
  if (hours < 24) return `${hours} h ago`
  const days = Math.floor(hours / 24)
  if (days < 30) return `${days} d ago`
  return formatDate(date, now)
}

function capitalize(s: string): string {
  return s.charAt(0).toUpperCase() + s.slice(1)
}

const SOURCE_LABELS: Record<string, string> = {
  laravel: 'Laravel',
  browser: 'Browser',
  php: 'PHP',
  node: 'Node.js',
  javascript: 'JavaScript',
}

/** sourceLabel names where an issue came from: "Laravel", "Browser". */
export function sourceLabel(source: string): string {
  return SOURCE_LABELS[source] ?? capitalize(source)
}

const RUNTIME_LABELS: Record<string, string> = { php: 'PHP', node: 'Node.js' }

/** majorMinor shortens a version to its first two numbers, as the Details card shows it: "8.4" for 8.4.1. */
export function majorMinor(version: string): string {
  return /^\D*\d+(\.\d+)?/.exec(version)?.[0] ?? version
}

/** runtimeLabel names a runtime for the Details card: "PHP". */
export function runtimeLabel(name: string): string {
  return RUNTIME_LABELS[name.toLowerCase()] ?? name
}

/**
 * displayFile shows a stack frame's file: a project path without its leading
 * slash (app/Http/Controllers/CheckoutController.php), a URL as its path.
 */
export function displayFile(file: string): string {
  let path = file
  if (/^[a-z][a-z0-9+.-]*:\/\//i.test(file)) {
    try {
      path = new URL(file).pathname
    } catch {
      // Not a URL after all.
    }
  }
  return path.replace(/^\/+/, '')
}

/** shortFunction keeps the method of a PHP class: "store" for App\…\CheckoutController::store. */
export function shortFunction(name: string): string {
  const i = name.lastIndexOf('::')
  return i >= 0 ? name.slice(i + 2) : name
}

/** countryName names an ISO 3166 code in English: "Netherlands" for NL. */
export function countryName(code: string): string {
  try {
    return new Intl.DisplayNames(['en'], { type: 'region' }).of(code) ?? code
  } catch {
    return code
  }
}

/** hiddenFramesSentence is the bar under a stack trace: "14 framework lines hidden. Show them." */
export function hiddenFramesSentence(hidden: number): string {
  return hidden === 1 ? '1 framework line hidden. Show it.' : `${hidden} framework lines hidden. Show them.`
}

/** urlPath shows the path and query of a URL ("/cart?step=2"); anything else as it is. */
export function urlPath(url: string): string {
  try {
    const u = new URL(url)
    return u.pathname + u.search
  } catch {
    return url
  }
}
