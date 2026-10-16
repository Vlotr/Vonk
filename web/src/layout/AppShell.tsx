import { useEffect, useState } from 'react'
import { Outlet, useMatch } from 'react-router'
import { getProjects } from '../api/client'
import { usePolling } from '../lib/usePolling'
import styles from './AppShell.module.css'
import { ProjectsContext } from './ProjectsContext'
import { Sidebar } from './Sidebar'

// AppShell is the frame around every page: the sidebar on the left and the
// current page on the right. It loads the project list for both.
export function AppShell() {
  const projects = usePolling(getProjects, 'projects')
  const id = useMatch('/projects/:id/*')?.params.id

  // The sidebar shows the project of the URL; on pages without one (All
  // projects) it keeps the last project visited, or else the first.
  const routeProjectId = id === undefined ? undefined : Number(id)
  const [lastProjectId, setLastProjectId] = useState<number>()
  useEffect(() => {
    if (routeProjectId !== undefined) setLastProjectId(routeProjectId)
  }, [routeProjectId])
  const list = projects.data ?? []
  const current =
    list.find((p) => p.id === (routeProjectId ?? lastProjectId)) ?? (routeProjectId === undefined ? list[0] : undefined)

  return (
    <ProjectsContext.Provider value={projects}>
      <div className={styles.shell}>
        <Sidebar project={current} />
        <main className={styles.main}>
          <Outlet />
        </main>
      </div>
    </ProjectsContext.Provider>
  )
}
