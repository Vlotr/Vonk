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
