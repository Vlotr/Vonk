import { useCallback, useEffect, useRef, useState } from 'react'

/** POLL_INTERVAL_MS is how often pages fetch fresh data. The server writes events every second. */
export const POLL_INTERVAL_MS = 60_000

export interface Polled<T> {
  data?: T
  error?: unknown
  /** loadedAt is when data last arrived. */
  loadedAt?: Date
  /** stale is true while data belongs to an older refreshKey and the new load is under way. */
  stale: boolean
  /** reload loads now, keeping the current data until the answer arrives. */
  reload: () => void
  /**
   * mutate changes data at once (an optimistic update) and holds back loads
   * until the function it returns is called, when the request behind the
   * change is done; that call loads fresh data. A poll in between could
   * bring back what the user just changed.
   */
  mutate: (change: (data: T) => T) => () => void
}

interface State<T> {
  key: string
  refreshKey: string
  data?: T
  error?: unknown
  loadedAt?: Date
}

/**
 * usePolling calls load now and then every minute.
 *
 * key names what is loaded: when it changes (another project, another
 * issue) the old data is dropped at once instead of showing under the new
 * URL. refreshKey names how it is loaded (filters, page size): when only it
 * changes, the data is loaded again but the old data stays, marked stale,
 * until the new data arrives. A failed refresh keeps the data that was
 * already there.
 */
export function usePolling<T>(load: (signal: AbortSignal) => Promise<T>, key: string, refreshKey = ''): Polled<T> {
  const [state, setState] = useState<State<T>>({ key, refreshKey })
  const loadRef = useRef(load)
  const controller = useRef<AbortController | null>(null)
  const pending = useRef(0) // mutations whose request is not done yet
  const run = useRef<() => void>(() => {})

  // The latest load closure (with the latest filters), for reload and the timer.
  useEffect(() => {
    loadRef.current = load
  })

  useEffect(() => {
    run.current = () => {
      if (pending.current > 0) return
      controller.current?.abort()
      const current = new AbortController()
      controller.current = current
      loadRef.current(current.signal).then(
        (data) => {
          if (!current.signal.aborted) setState({ key, refreshKey, data, loadedAt: new Date() })
        },
        (error: unknown) => {
          if (!current.signal.aborted) setState((prev) => (prev.key === key ? { ...prev, error } : { key, refreshKey, error }))
        },
      )
    }
    run.current()
    const timer = setInterval(() => run.current(), POLL_INTERVAL_MS)
    return () => {
      clearInterval(timer)
      controller.current?.abort()
    }
  }, [key, refreshKey])

  const reload = useCallback(() => run.current(), [])

  const mutate = useCallback((change: (data: T) => T) => {
    pending.current++
    controller.current?.abort() // an answer from before the change is out of date
    setState((prev) => (prev.data === undefined ? prev : { ...prev, data: change(prev.data) }))
    let settled = false
    return () => {
      if (settled) return
      settled = true
      pending.current--
      if (pending.current === 0) run.current()
    }
  }, [])

  const current: State<T> = state.key === key ? state : { key, refreshKey }
  return {
    data: current.data,
    error: current.error,
    loadedAt: current.loadedAt,
    stale: current.data !== undefined && current.refreshKey !== refreshKey,
    reload,
    mutate,
  }
}
