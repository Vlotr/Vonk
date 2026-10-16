import type { MouseEvent } from 'react'
import { Link, useNavigate } from 'react-router'
import type { Project } from '../api/client'
import { LoadError } from '../components/LoadError'
import { Tag } from '../components/Tag'
import { useProjects } from '../layout/ProjectsContext'
import { formatTime, projectsHeadline, sourceLabel } from '../lib/format'
import page from './Page.module.css'
import styles from './ProjectsPage.module.css'

// ProjectsPage lists every project with its open issues. Task 4 turns it
// into the All projects overview of the design.
export function ProjectsPage() {
  const { data: projects, error, loadedAt } = useProjects()
  if (!projects) return <section aria-busy={!error}>{error ? <LoadError /> : null}</section>

  return (
    <section>
      <h1 className={page.title}>{projectsHeadline(projects)}</h1>
      {loadedAt && (
        <p className={page.lead}>Updated {formatTime(loadedAt)}. New data comes in every minute.</p>
      )}

      <div className={`${page.card} ${styles.card}`}>
        <h2 className={`${page.cardTitle} ${styles.cardTitle}`}>Projects</h2>
        {projects.length === 0 ? (
          <p className={page.empty}>
            Create one on the server with <code>monitor project create --name Webshop</code>. It prints the DSN to give
            your app's Sentry SDK.
          </p>
        ) : (
          <div className={page.scroll}>
            <table className={page.table}>
              <thead>
                <tr>
                  <th scope="col">Project</th>
                  <th scope="col" className={page.fit}>
                    Open issues
                  </th>
                </tr>
              </thead>
              <tbody>
                {projects.map((p) => (
                  <ProjectRow key={p.id} project={p} />
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>
    </section>
  )
}

function ProjectRow({ project }: { project: Project }) {
  const navigate = useNavigate()
  const href = `/projects/${project.id}/issues`
  const onRowClick = (e: MouseEvent) => {
    if (e.target instanceof Element && e.target.closest('a')) return
    navigate(href)
  }
  return (
    <tr className={page.row} onClick={onRowClick}>
      <td>
        <Link to={href} className={styles.name}>
          {project.name}
        </Link>
        {project.sources.length > 0 && (
          <div className={styles.tags}>
            {project.sources.map((s) => (
              <Tag key={s}>{sourceLabel(s)}</Tag>
            ))}
          </div>
        )}
      </td>
      <td className={page.fit}>
        {project.openIssues > 0 ? project.openIssues.toLocaleString('en-US') : <span className={styles.none}>None</span>}
      </td>
    </tr>
  )
}
