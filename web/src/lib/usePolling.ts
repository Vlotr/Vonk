import { useEffect, useState } from 'react'

/** POLL_INTERVAL_MS is how often pages fetch fresh data. The server writes events every second. */
export const POLL_INTERVAL_MS = 60_000

export interface Polled<T> {
  data?: T
  error?: unknown
  /** loadedAt is when data last arrived. */
  loadedAt?: Date
}

/**
 * usePolling calls load now and then every minute. key names what is
 * loaded: when it changes (another project, another issue) the old data is
 * dropped at once instead of showing under the new URL. A failed refresh
 * keeps the data that was already there.
 */
export function usePolling<T>(load: (signal: AbortSignal) => Promise<T>, key: string): Polled<T> {
  const [state, setState] = useState<Polled<T> & { key: string }>({ key })

  useEffect(() => {
    let controller = new AbortController()
    const run = () => {
      controller.abort()
      const current = new AbortController()
      controller = current
      load(current.signal).then(
        (data) => {
          if (!current.signal.aborted) setState({ key, data, loadedAt: new Date() })
        },
        (error: unknown) => {
          if (!current.signal.aborted) setState((prev) => ({ ...(prev.key === key ? prev : {}), key, error }))
        },
      )
    }
    run()
    const timer = setInterval(run, POLL_INTERVAL_MS)
    return () => {
      clearInterval(timer)
      controller.abort()
    }
    // load is a new closure on every render; key says when it really changed.
  }, [key])

  return state.key === key ? state : {}
}
