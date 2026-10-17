// Text for the Settings screens. Pure functions, tested in settings.test.ts.

import { plural } from './format'

/** RETENTION_CHOICES are the "Keep data for" options; null keeps data indefinitely. */
export const RETENTION_CHOICES: { days: number | null; label: string }[] = [
  { days: 30, label: '30 days' },
  { days: 90, label: '90 days' },
  { days: 180, label: '180 days' },
  { days: 365, label: '1 year' },
  { days: null, label: 'Indefinitely' },
]

/** retentionLabel writes how long a project keeps data: "90 days", "1 year", "Indefinitely". */
export function retentionLabel(days: number | null): string {
  if (days === null) return 'Indefinitely'
  return RETENTION_CHOICES.find((c) => c.days === days)?.label ?? plural(days, 'day')
}

interface Tracking {
  tracksLaravel: boolean
  tracksBrowser: boolean
  tracksPageviews: boolean
}

/** trackingLabels are the chips of "Tracks": Laravel errors, Browser errors, Page views. */
export function trackingLabels(p: Tracking): string[] {
  const labels: string[] = []
  if (p.tracksLaravel) labels.push('Laravel errors')
  if (p.tracksBrowser) labels.push('Browser errors')
  if (p.tracksPageviews) labels.push('Page views')
  return labels
}

/** roleLabel shows a role: "Admin" or "Member". */
export function roleLabel(role: string): string {
  return role === 'admin' ? 'Admin' : 'Member'
}

/** expiresIn says how long a link still works: "in 7 days", "in 23 hours", "in 5 minutes". */
export function expiresIn(expires: Date, now: Date): string {
  const minutes = Math.floor((expires.getTime() - now.getTime()) / 60_000)
  if (minutes <= 0) return 'expired'
  if (minutes < 60) return `in ${plural(minutes, 'minute')}`
  const hours = Math.floor(minutes / 60)
  if (hours < 24) return `in ${plural(hours, 'hour')}`
  return `in ${plural(Math.floor(hours / 24), 'day')}`
}

/** laravelEnvLine is the line for a Laravel app's .env file. */
export function laravelEnvLine(dsn: string): string {
  return `SENTRY_LARAVEL_DSN=${dsn}`
}

/** browserSnippet sets up @sentry/browser with the project's DSN. */
export function browserSnippet(dsn: string): string {
  return `import * as Sentry from '@sentry/browser'

Sentry.init({
  dsn: '${dsn}',
})`
}

/**
 * posthogSnippet sets up posthog-js the way this tool was tested with
 * (posthog-js 1.438): no cookies (persistence 'memory'), page views on
 * load and on history changes, page leaves for the time on a page, no
 * session recording, sending to this server.
 */
export function posthogSnippet(token: string, host: string): string {
  return `import posthog from 'posthog-js'

posthog.init('${token}', {
  api_host: '${host}',
  persistence: 'memory',
  capture_pageview: 'history_change',
  capture_pageleave: true,
  disable_session_recording: true,
})`
}

/** identifySnippet gives the browser SDKs the signed-in user's id, so errors and page views of one person match. */
export function identifySnippet(): string {
  return `// After sign-in (and on every page load while signed in):
posthog.identify(String(user.id))
Sentry.setUser({ id: String(user.id) })

// After sign-out:
posthog.reset()
Sentry.setUser(null)`
}

/** LARAVEL_USER_SNIPPET gives Laravel's errors the signed-in user's id (only the id), in AppServiceProvider::boot(). */
export const LARAVEL_USER_SNIPPET = `use Illuminate\\Auth\\Events\\Authenticated;
use Illuminate\\Support\\Facades\\Event;

Event::listen(function (Authenticated $event) {
    \\Sentry\\configureScope(function (\\Sentry\\State\\Scope $scope) use ($event) {
        $scope->setUser(['id' => (string) $event->user->getAuthIdentifier()]);
    });
});`

/** patternsText shows route patterns one per line, for the Edit project field. */
export function patternsText(patterns: string[]): string {
  return patterns.join('\n')
}

/** parsePatterns reads the Edit project field: one pattern per line, blank lines left out. */
export function parsePatterns(text: string): string[] {
  return text
    .split(/\r?\n/)
    .map((line) => line.trim())
    .filter((line) => line !== '')
}

/** The events-per-minute limits Edit project takes; a new project starts at 1000. */
export const RATE_LIMIT_MIN = 10
export const RATE_LIMIT_MAX = 100_000

/** parseRateLimit reads the events-per-minute field: a whole number from 10 to 100 000 (spaces between digits allowed), else null. */
export function parseRateLimit(text: string): number | null {
  const digits = text.trim().replace(/ /g, '')
  if (!/^\d+$/.test(digits)) return null
  const limit = Number(digits)
  return limit >= RATE_LIMIT_MIN && limit <= RATE_LIMIT_MAX ? limit : null
}

/** rateLimitSentence says what a project's limit does (Setup and keys). Sentry and PostHog traffic each have their own count. */
export function rateLimitSentence(limit: number): string {
  return `It accepts up to ${limit.toLocaleString('en-US')} events a minute from the Sentry SDKs, and as many from the PostHog SDK; more are refused (429) until the next minute.`
}
