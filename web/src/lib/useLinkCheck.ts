import { useEffect, useState } from 'react'
import { ApiError } from '../api/client'

/** LinkCheck is what an invite or reset link turned out to be. */
export type LinkCheck<T> =
  | { state: 'checking' }
  | { state: 'ok'; data: T }
  /** gone: it expired or was used (410). */
  | { state: 'gone' }
  /** invalid: no token, or one the server does not know (404). */
  | { state: 'invalid' }
  | { state: 'failed'; error: unknown }

/** useLinkCheck asks the server about the token of an invite or reset link, once. */
export function useLinkCheck<T>(token: string, check: (token: string) => Promise<T>): LinkCheck<T> {
  const [result, setResult] = useState<LinkCheck<T>>(token ? { state: 'checking' } : { state: 'invalid' })
  useEffect(() => {
    if (!token) return
    let current = true
    check(token).then(
      (data) => current && setResult({ state: 'ok', data }),
      (error: unknown) => {
        if (!current) return
        if (error instanceof ApiError && error.status === 410) setResult({ state: 'gone' })
        else if (error instanceof ApiError && error.status === 404) setResult({ state: 'invalid' })
        else setResult({ state: 'failed', error })
      },
    )
    return () => {
      current = false
    }
    // check is a module function; the token decides.
  }, [token])
  return result
}
