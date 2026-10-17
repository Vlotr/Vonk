import { useEffect, useState } from 'react'
import { Outlet, useMatch } from 'react-router'
import { getMe, getProjects, setCsrfToken, setSignedOutHandler } from '../api/client'
import { usePolling } from '../lib/usePolling'
import styles from './AppShell.module.css'
import { MeContext } from './MeContext'
import { ProjectsContext } from './ProjectsContext'
import { Sidebar } from './Sidebar'

// AppShell is the frame around every page of a signed-in user: the sidebar
// on the left and the current page on the right. It loads the user and the
// projects they may see, for both.
export function AppShell() {
  const me = usePolling(getMe, 'me')
  const projects = usePolling(getProjects, 'projects')
  const id = useMatch('/projects/:id/*')?.params.id

  // Changes send the session's CSRF token back.
  const csrfToken = me.data?.csrfToken
  useEffect(() => {
    if (csrfToken) setCsrfToken(csrfToken)
  }, [csrfToken])

  // A session that ends (it expired, or an admin removed the account) sends
  // the next request to the login page, and back here after signing in.
  useEffect(() => {
    setSignedOutHandler(() => {
      const here = window.location.pathname + window.location.search
      window.location.assign(`/login?next=${encodeURIComponent(here)}`)
    })
    return () => setSignedOutHandler(() => {})
  }, [])

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
    <MeContext.Provider value={me.data}>
      <ProjectsContext.Provider value={projects}>
        <div className={styles.shell}>
          <Sidebar project={current} />
          <main className={styles.main}>
            <Outlet />
          </main>
        </div>
      </ProjectsContext.Provider>
    </MeContext.Provider>
  )
}
