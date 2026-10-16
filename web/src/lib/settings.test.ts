import { expect, test } from 'vitest'
import { browserSnippet, expiresIn, laravelEnvLine, retentionLabel, roleLabel, trackingLabels } from './settings'

test('retentionLabel names the Keep data for choices', () => {
  expect(retentionLabel(30)).toBe('30 days')
  expect(retentionLabel(90)).toBe('90 days')
  expect(retentionLabel(365)).toBe('1 year')
  expect(retentionLabel(null)).toBe('Indefinitely')
  expect(retentionLabel(1)).toBe('1 day')
})

test('trackingLabels lists what a project tracks, as the design orders the chips', () => {
  expect(trackingLabels({ tracksLaravel: true, tracksBrowser: true, tracksPageviews: true })).toEqual([
    'Laravel errors',
    'Browser errors',
    'Page views',
  ])
  expect(trackingLabels({ tracksLaravel: false, tracksBrowser: true, tracksPageviews: false })).toEqual(['Browser errors'])
})

test('roleLabel shows roles as the UI names them', () => {
  expect(roleLabel('admin')).toBe('Admin')
  expect(roleLabel('member')).toBe('Member')
})

test('expiresIn says how long a link still works', () => {
  const now = new Date('2026-10-16T12:00:00Z')
  expect(expiresIn(new Date('2026-10-23T12:00:00Z'), now)).toBe('in 7 days')
  expect(expiresIn(new Date('2026-10-17T11:00:00Z'), now)).toBe('in 23 hours')
  expect(expiresIn(new Date('2026-10-16T13:00:00Z'), now)).toBe('in 1 hour')
  expect(expiresIn(new Date('2026-10-16T12:05:00Z'), now)).toBe('in 5 minutes')
  expect(expiresIn(new Date('2026-10-16T11:00:00Z'), now)).toBe('expired')
})

test('setup snippets carry the DSN', () => {
  const dsn = 'https://0123abcd@monitor.example.com/1'
  expect(laravelEnvLine(dsn)).toBe('SENTRY_LARAVEL_DSN=https://0123abcd@monitor.example.com/1')
  expect(browserSnippet(dsn)).toContain("import * as Sentry from '@sentry/browser'")
  expect(browserSnippet(dsn)).toContain(`dsn: '${dsn}'`)
})
