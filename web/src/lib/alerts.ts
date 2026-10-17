// Text and changes for the Alerts page. Pure functions, tested in alerts.test.ts.

import type { AlertKinds, AlertsChange, ProjectAlerts } from '../api/client'

/** The spike threshold is a whole multiple of the usual hourly errors, from 2 to 100. */
export const MIN_THRESHOLD = 2
export const MAX_THRESHOLD = 100

const KINDS: (keyof AlertKinds)[] = ['newIssue', 'regression', 'spike', 'digest']

/** emailActive: some kind is mailed, and the server can mail. */
function emailActive(a: ProjectAlerts): boolean {
  return a.mailEnabled && KINDS.some((k) => a.alerts[k].email)
}

/** slackActive: some kind goes to Slack, and there is a webhook. */
function slackActive(a: ProjectAlerts): boolean {
  return a.slackWebhook !== null && KINDS.some((k) => a.alerts[k].slack)
}

/** alertsHeadline is the conclusion: "Webshop sends alerts by email and Slack". */
export function alertsHeadline(a: ProjectAlerts): string {
  const email = emailActive(a)
  const slack = slackActive(a)
  const name = a.project.name
  if (email && slack) return `${name} sends alerts by email and Slack`
  if (email) return `${name} sends alerts by email`
  if (slack) return `${name} sends alerts to Slack`
  return `${name} sends no alerts`
}

/** alertsLead says who gets the mail and whether Slack still needs a webhook. */
export function alertsLead(a: ProjectAlerts): string {
  const parts: string[] = []
  if (a.mailEnabled) {
    const people = a.recipients === 1 ? '1 person' : `${a.recipients} people`
    parts.push(`Email goes to the ${people} who can see ${a.project.name}.`)
  }
  if (a.slackWebhook !== null) {
    parts.push('Slack messages go to the webhook below.')
  } else {
    parts.push(a.mailEnabled ? 'Add a Slack webhook below to send alerts to Slack too.' : 'Add a Slack webhook below to send alerts to Slack.')
  }
  return parts.join(' ')
}

/** withAlertsChange is the page's data after a change, before the server confirms it. */
export function withAlertsChange(a: ProjectAlerts, change: AlertsChange): ProjectAlerts {
  const next: ProjectAlerts = { ...a, alerts: { ...a.alerts } }
  const changed = change.alerts ?? {}
  if (changed.newIssue) next.alerts.newIssue = { ...a.alerts.newIssue, ...changed.newIssue }
  if (changed.regression) next.alerts.regression = { ...a.alerts.regression, ...changed.regression }
  if (changed.spike) next.alerts.spike = { ...a.alerts.spike, ...changed.spike }
  if (changed.digest) next.alerts.digest = { ...a.alerts.digest, ...changed.digest }
  if (change.slackWebhookUrl === '') next.slackWebhook = null
  return next
}

/** parseThreshold reads the threshold field: a whole number from 2 to 100, else null. */
export function parseThreshold(text: string): number | null {
  const t = text.trim()
  if (!/^\d+$/.test(t)) return null
  const n = Number(t)
  return n >= MIN_THRESHOLD && n <= MAX_THRESHOLD ? n : null
}
