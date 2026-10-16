import { expect, test } from 'vitest'
import type { ProjectAlerts } from '../api/client'
import { alertsHeadline, alertsLead, parseThreshold, withAlertsChange } from './alerts'

function alerts(over: Partial<ProjectAlerts> = {}, kinds: Partial<ProjectAlerts['alerts']> = {}): ProjectAlerts {
  const on = { email: true, slack: true }
  return {
    project: { id: 1, name: 'Webshop' },
    mailEnabled: true,
    recipients: 3,
    slackWebhook: 'https://hooks.slack.com/…/abcd',
    alerts: {
      newIssue: on,
      regression: on,
      spike: { ...on, threshold: 5 },
      digest: { email: false, slack: false, frequency: 'weekly' },
      ...kinds,
    },
    ...over,
  }
}

test('the headline says where alerts go', () => {
  expect(alertsHeadline(alerts())).toBe('Webshop sends alerts by email and Slack')
  expect(alertsHeadline(alerts({ slackWebhook: null }))).toBe('Webshop sends alerts by email')
  expect(alertsHeadline(alerts({ mailEnabled: false }))).toBe('Webshop sends alerts to Slack')
  expect(alertsHeadline(alerts({ mailEnabled: false, slackWebhook: null }))).toBe('Webshop sends no alerts')
  const off = { email: false, slack: false }
  expect(
    alertsHeadline(alerts({}, { newIssue: off, regression: off, spike: { ...off, threshold: 5 }, digest: { ...off, frequency: 'daily' } })),
  ).toBe('Webshop sends no alerts')
  // Slack switched on for one kind only still counts.
  expect(
    alertsHeadline(
      alerts({ mailEnabled: false }, { newIssue: off, regression: off, spike: { ...off, threshold: 5 }, digest: { email: false, slack: true, frequency: 'daily' } }),
    ),
  ).toBe('Webshop sends alerts to Slack')
})

test('the lead says who gets the mail and what is missing', () => {
  expect(alertsLead(alerts())).toBe('Email goes to the 3 people who can see Webshop. Slack messages go to the webhook below.')
  expect(alertsLead(alerts({ recipients: 1, slackWebhook: null }))).toBe(
    'Email goes to the 1 person who can see Webshop. Add a Slack webhook below to send alerts to Slack too.',
  )
  expect(alertsLead(alerts({ mailEnabled: false, slackWebhook: null }))).toBe('Add a Slack webhook below to send alerts to Slack.')
})

test('a change shows at once', () => {
  const before = alerts()
  const after = withAlertsChange(before, { alerts: { spike: { threshold: 10 }, newIssue: { email: false } } })
  expect(after.alerts.spike).toEqual({ email: true, slack: true, threshold: 10 })
  expect(after.alerts.newIssue).toEqual({ email: false, slack: true })
  expect(after.alerts.regression).toBe(before.alerts.regression)
  expect(withAlertsChange(before, { slackWebhookUrl: '' }).slackWebhook).toBeNull()
})

test('the spike threshold is a whole number from 2 to 100', () => {
  expect(parseThreshold('5')).toBe(5)
  expect(parseThreshold(' 20 ')).toBe(20)
  expect(parseThreshold('1')).toBeNull()
  expect(parseThreshold('101')).toBeNull()
  expect(parseThreshold('2.5')).toBeNull()
  expect(parseThreshold('')).toBeNull()
})
