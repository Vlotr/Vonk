import { createContext, useContext } from 'react'
import type { Project } from '../api/client'
import type { Polled } from '../lib/usePolling'

/** ProjectsContext shares the project list the shell loads with the sidebar and the pages. */
export const ProjectsContext = createContext<Polled<Project[]>>({})

export function useProjects(): Polled<Project[]> {
  return useContext(ProjectsContext)
}
