import type { MouseEvent } from 'react'
import { Link, useNavigate } from 'react-router'
import { getOverview, type NewestIssue, type Overview, type OverviewProject, updateProject } from '../api/client'
import { HourBars, HourLegend } from '../components/HourBars'
import { LoadError } from '../components/LoadError'
import { Tag } from '../components/Tag'
import { useMe } from '../layout/MeContext'
import { formatDuration, formatTime, overviewHeadline, shortType, sourceLabel, timeAgo } from '../lib/format'
import { usePolling } from '../lib/usePolling'
import styles from './AllProjectsPage.module.css'
import page from './Page.module.css'

// AllProjectsPage is the overview: per project the new issues and errors of
// the last 24 hours with a bar per hour, its page views and average time on
// a page, then the newest issues of all projects. For projects without page
// tracking the page columns say "Add page tracking", which for admins turns
// page views on and opens the project's Setup and keys.
export function AllProjectsPage() {
  const { data, error } = usePolling(getOverview, 'overview')
  if (!data) return <section aria-busy={!error}>{error ? <LoadError /> : null}</section>

  return (
    <section>
      <h1 className={`${page.title} ${styles.headline}`}>{overviewHeadline(data.projects)}</h1>
      <p className={page.lead}>Updated {formatTime(new Date(data.updatedAt))}. New data comes in every minute.</p>
      <ProjectsCard overview={data} />
      {data.projects.length > 0 && <NewestIssuesCard issues={data.newestIssues} />}
    </section>
  )
}

function ProjectsCard({ overview }: { overview: Overview }) {
  const me = useMe()
  // One scale for the whole table, so a quiet project stays light next to a busy one.
  const busiestHour = Math.max(0, ...overview.projects.flatMap((p) => p.hourly))
  return (
    <section className={`${page.card} ${styles.card}`} aria-labelledby="projects-title">
      <header className={styles.cardHeader}>
        <h2 id="projects-title" className={page.cardTitle}>
          Projects
        </h2>
        {overview.projects.length > 0 && <HourLegend label="Errors per hour" />}
      </header>
      {overview.projects.length === 0 ? (
        me?.role === 'admin' ? (
          <p className={page.empty}>
            <Link to="/settings/projects?new">Add your first project</Link> in Settings. It gives you the DSN for your app's Sentry SDK.
          </p>
        ) : (
          <p className={page.empty}>You do not see any project yet. Ask an admin to give you access.</p>
        )
      ) : (
        <div className={page.scroll}>
          <table className={`${page.table} ${styles.table}`}>
            <thead>
              <tr>
                <th scope="col">Project</th>
                <th scope="col" className={styles.newColumn}>
                  New issues
                </th>
                <th scope="col" className={styles.errorsColumn}>
                  Errors, 24 h
                </th>
                <th scope="col" className={styles.barsColumn}>
                  Last 24 hours
                </th>
                <th scope="col" className={styles.viewsColumn}>
                  Page views, 24 h
                </th>
                <th scope="col" className={styles.timeColumn}>
                  Avg. time on page
                </th>
              </tr>
            </thead>
            <tbody>
              {overview.projects.map((p) => (
                <ProjectRow key={p.id} project={p} busiestHour={busiestHour} isAdmin={me?.role === 'admin'} />
              ))}
            </tbody>
          </table>
        </div>
      )}
    </section>
  )
}

function ProjectRow({ project, busiestHour, isAdmin }: { project: OverviewProject; busiestHour: number; isAdmin: boolean }) {
  const navigate = useNavigate()
  const href = `/projects/${project.id}/issues`
  const onRowClick = (e: MouseEvent) => {
    if (e.target instanceof Element && e.target.closest('a, button')) return
    if (window.getSelection()?.toString()) return
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
      <td>
        {project.newIssues24h > 0 ? (
          <span className={styles.newCount}>{project.newIssues24h.toLocaleString('en-US')} new</span>
        ) : (
          <span className={styles.none}>None</span>
        )}
      </td>
      <td>{project.errors24h.toLocaleString('en-US')}</td>
      <td>
        <HourBars counts={project.hourly} variant="wide" max={busiestHour} noun="error" />
      </td>
      {project.pageviews24h === null ? (
        <td colSpan={2}>
          {isAdmin ? (
            <button
              type="button"
              className={styles.addTracking}
              onClick={async () => {
                // If this fails, the setup page says page views are off and offers to turn them on.
                await updateProject(project.id, { tracksPageviews: true }).catch(() => {})
                navigate(`/settings/projects/${project.id}/setup#page-views`)
              }}
            >
              Add page tracking
            </button>
          ) : (
            <span className={styles.none}>No page tracking yet</span>
          )}
        </td>
      ) : (
        <>
          <td>{project.pageviews24h.toLocaleString('en-US')}</td>
          <td>{project.avgTimeOnPageMs === null ? '' : formatDuration(project.avgTimeOnPageMs)}</td>
        </>
      )}
    </tr>
  )
}

function NewestIssuesCard({ issues }: { issues: NewestIssue[] }) {
  const now = new Date()
  const newest = issues[0]
  return (
    <section className={`${page.card} ${styles.newestCard}`} aria-labelledby="newest-title">
      <header className={styles.newestHeader}>
        <h2 id="newest-title" className={page.cardTitle}>
          Newest issues
        </h2>
        {newest && (
          <Link to={`/projects/${newest.projectId}/issues`} className={styles.seeAll}>
            See all issues
          </Link>
        )}
      </header>
      {issues.length === 0 ? (
        <p className={`${page.empty} ${styles.newestEmpty}`}>No new issues in the last 24 hours.</p>
      ) : (
        <table className={`${page.table} ${styles.newestTable}`}>
          <tbody>
            {issues.map((issue) => (
              <NewestIssueRow key={issue.id} issue={issue} now={now} />
            ))}
          </tbody>
        </table>
      )}
    </section>
  )
}

function NewestIssueRow({ issue, now }: { issue: NewestIssue; now: Date }) {
  const navigate = useNavigate()
  const href = `/projects/${issue.projectId}/issues/${issue.id}`
  const lastSeen = new Date(issue.lastSeen)
  const onRowClick = (e: MouseEvent) => {
    if (e.target instanceof Element && e.target.closest('a')) return
    if (window.getSelection()?.toString()) return
    navigate(href)
  }
  return (
    <tr className={page.row} onClick={onRowClick}>
      <td className={styles.issueCell}>
        <Link to={href} className={styles.issue}>
          <strong title={issue.type}>{shortType(issue.type)}</strong>
          {issue.message && issue.message !== issue.type ? ` ${issue.message}` : ''}
        </Link>
      </td>
      <td className={`${page.fit} ${styles.muted}`}>{issue.projectName}</td>
      <td className={`${page.fit} ${styles.muted} ${styles.ago}`}>
        <time dateTime={issue.lastSeen} title={lastSeen.toLocaleString()}>
          {timeAgo(lastSeen, now)}
        </time>
      </td>
    </tr>
  )
}
