import { expect, test } from 'vitest'
import {
  browserSnippet,
  expiresIn,
  identifySnippet,
  LARAVEL_USER_SNIPPET,
  laravelEnvLine,
  parsePatterns,
  patternsText,
  posthogSnippet,
  retentionLabel,
  roleLabel,
  trackingLabels,
} from './settings'

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

// The setup Task 7 proved with posthog-js 1.438 (internal/posthog/testdata/capture/src/app.js).
test('the PostHog snippet is the proven setup: cookieless, page views on history changes, page leave on, no recording', () => {
  expect(posthogSnippet('phc_abc', 'https://monitor.example.com')).toBe(`import posthog from 'posthog-js'

posthog.init('phc_abc', {
  api_host: 'https://monitor.example.com',
  persistence: 'memory',
  capture_pageview: 'history_change',
  capture_pageleave: true,
  disable_session_recording: true,
})`)
})

test('the same user id goes to both SDKs', () => {
  expect(identifySnippet()).toContain('posthog.identify(String(user.id))')
  expect(identifySnippet()).toContain('Sentry.setUser({ id: String(user.id) })')
  expect(LARAVEL_USER_SNIPPET).toContain("$scope->setUser(['id' => (string) $event->user->getAuthIdentifier()])")
})

test('route patterns are edited one per line', () => {
  expect(patternsText(['/products/:slug', '/blog/:year/:post'])).toBe('/products/:slug\n/blog/:year/:post')
  expect(parsePatterns(' /products/:slug \n\n  \n/blog/:year/:post\r\n')).toEqual(['/products/:slug', '/blog/:year/:post'])
  expect(parsePatterns('')).toEqual([])
})
