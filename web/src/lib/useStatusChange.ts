import { useState } from 'react'
import { type IssueStatus, setIssueStatus } from '../api/client'
import { useProjects } from '../layout/ProjectsContext'
import type { Polled } from './usePolling'

/**
 * useStatusChange resolves, ignores or reopens an issue for a page that
 * polls its data: the page's data changes at once (optimistic), polls wait
 * until the request is done, then the page and the sidebar's open counts
 * load again. A failure leaves one sentence in `failed`, and the reload
 * brings the old state back.
 */
export function useStatusChange<T>(polled: Polled<T>) {
  const projects = useProjects()
  const [failed, setFailed] = useState('')

  /** change sets the issue to `to`; name is how the failure sentence calls it ("QueryException", "The issue"). */
  const change = async (issueId: number, to: IssueStatus, optimistic: (data: T) => T, name: string) => {
    setFailed('')
    const settle = polled.mutate(optimistic)
    try {
      await setIssueStatus(issueId, to)
      projects.reload()
    } catch {
      setFailed(`${name} could not be ${to === 'open' ? 'reopened' : to}. Try again.`)
    } finally {
      settle() // reloads, which also undoes the change when it failed
    }
  }

  return { failed, clearFailed: () => setFailed(''), change }
}
