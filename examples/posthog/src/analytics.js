// Page views and the shop's own events with posthog-js and errors with
// @sentry/browser, both sent to the monitoring tool, and both told the same
// user id. The setup is the one the tool's "Setup and keys" page gives. The
// keys (and the release) come from the environment when Vite starts (see
// README.md).
import * as Sentry from '@sentry/browser'
import posthog from 'posthog-js'

const token = import.meta.env.VITE_POSTHOG_TOKEN
const host = import.meta.env.VITE_POSTHOG_HOST
const dsn = import.meta.env.VITE_SENTRY_DSN
const release = import.meta.env.VITE_SENTRY_RELEASE

if (token && host) {
  posthog.init(token, {
    api_host: host,
    persistence: 'memory',
    capture_pageview: 'history_change',
    capture_pageleave: true,
    disable_session_recording: true,
  })
}
if (dsn) {
  Sentry.init({ dsn, release })
}

// The shop's own sign-in: a real app knows the user from its session on
// every page load; this example keeps the id in sessionStorage.
const USER_KEY = 'example-user-id'

function identify(id) {
  posthog.identify(String(id))
  Sentry.setUser({ id: String(id) })
}

const signedIn = sessionStorage.getItem(USER_KEY)
if (signedIn) identify(signedIn)

export function signIn(id) {
  sessionStorage.setItem(USER_KEY, String(id))
  identify(id)
}

export function signOut() {
  sessionStorage.removeItem(USER_KEY)
  posthog.reset()
  Sentry.setUser(null)
}

export function currentUser() {
  return sessionStorage.getItem(USER_KEY)
}

// The shop's own events (Events in the dashboard, and the user's activity).
export function track(name, properties) {
  posthog.capture(name, properties)
}

export const configured = Boolean(token && host && dsn)
