import { createContext, useContext } from 'react'
import type { Project } from '../api/client'
import type { Polled } from '../lib/usePolling'

/**
 * ProjectsContext shares the project list the shell loads with the sidebar
 * and the pages. A page that changes an issue's status calls reload, so the
 * open-issue count in the sidebar follows.
 */
export const ProjectsContext = createContext<Polled<Project[]>>({
  stale: false,
  reload: () => {},
  mutate: () => () => {},
})

export function useProjects(): Polled<Project[]> {
  return useContext(ProjectsContext)
}
