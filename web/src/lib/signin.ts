// Helpers for the sign-in pages. Pure functions, tested in signin.test.ts.

import { ApiError } from '../api/client'

/** The pages that sign people in; after signing in, the app never goes back to them. */
const SIGN_IN_PAGES = ['/login', '/setup', '/invite', '/forgot-password', '/reset-password']

/**
 * safeNext returns where to go after signing in: the ?next= path when it is
 * a page of this site, else "/". Anything that could lead to another site
 * ("//evil.example.com", "https://…", "/\evil") is refused, so a crafted
 * login link cannot send someone elsewhere.
 */
export function safeNext(next: string | null): string {
  if (!next || !next.startsWith('/') || next.startsWith('//') || /[\\\s]/.test(next)) return '/'
  const path = next.split(/[?#]/)[0]
  if (SIGN_IN_PAGES.includes(path)) return '/'
  return next
}

/**
 * tokenFromHash reads the token of an invite or reset link. Links carry it
 * after "#token=", the part of a URL browsers never send to a server, so it
 * shows up in no server or proxy log.
 */
export function tokenFromHash(hash: string): string {
  return new URLSearchParams(hash.replace(/^#/, '')).get('token') ?? ''
}

/** problem turns a failed request into a sentence for the person at the form. */
export function problem(err: unknown): string {
  if (err instanceof ApiError) {
    if (err.status >= 500) return 'Something went wrong on the server. Try again in a moment.'
    if (err.message && /^[A-Z]/.test(err.message)) return err.message // the server's own words, written for people
    return 'That did not work. Check what you typed and try again.'
  }
  return 'The server cannot be reached. Check your connection and try again.'
}
