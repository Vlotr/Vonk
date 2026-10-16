import type { MouseEvent } from 'react'
import { Link, useNavigate, useParams } from 'react-router'
import { ApiError, getIssues, type Issue, type IssueList } from '../api/client'
import { HourBars } from '../components/HourBars'
import { LoadError } from '../components/LoadError'
import { Tag } from '../components/Tag'
import {
  issuesHeadline,
  newIssuesSentence,
  shortType,
  showingSentence,
  sourceLabel,
  timeAgo,
} from '../lib/format'
import { usePolling } from '../lib/usePolling'
import { NotFoundPage } from './NotFoundPage'
import styles from './IssuesPage.module.css'
import page from './Page.module.css'

// IssuesPage lists a project's open issues, the most recently seen first.
export function IssuesPage() {
  const projectId = Number(useParams().id)
  const { data, error } = usePolling((signal) => getIssues(projectId, {}, signal), `issues-${projectId}`)

  if (error instanceof ApiError && error.status === 404) return <NotFoundPage what="Project" />
  if (!data) return <section aria-busy={!error}>{error ? <LoadError /> : null}</section>

  const open = data.counts.open
  return (
    <section>
      <nav className={page.breadcrumb} aria-label="Breadcrumb">
        <Link to="/">All projects</Link>
      </nav>
      <h1 className={page.title}>{issuesHeadline(open, data.project.name)}</h1>
      <p className={page.lead}>{newIssuesSentence(open, data.new24h)}</p>

      <div className={`${page.card} ${styles.card}`}>
        {data.issues.length > 0 ? <IssueTable list={data} /> : <EmptyIssues list={data} />}
      </div>
    </section>
  )
}

function IssueTable({ list }: { list: IssueList }) {
  const now = new Date()
  return (
    <>
      <div className={page.scroll}>
        <table className={page.table}>
          <thead>
            <tr>
              <th scope="col">Issue</th>
              <th scope="col" className={styles.barsColumn}>
                Last 24 hours
              </th>
              <th scope="col" className={page.fit}>
                Events
              </th>
              <th scope="col" className={page.fit}>
                Users
              </th>
              <th scope="col" className={page.fit}>
                Last seen
              </th>
            </tr>
          </thead>
          <tbody>
            {list.issues.map((issue) => (
              <IssueRow key={issue.id} issue={issue} projectId={list.project.id} now={now} />
            ))}
          </tbody>
        </table>
      </div>
      <div className={styles.footer}>{showingSentence(list.issues.length, list.total)}</div>
    </>
  )
}

function IssueRow({ issue, projectId, now }: { issue: Issue; projectId: number; now: Date }) {
  const navigate = useNavigate()
  const href = `/projects/${projectId}/issues/${issue.id}`
  const lastSeen = new Date(issue.lastSeen)

  // The title is the link; a click anywhere else on the row opens the issue too.
  const onRowClick = (e: MouseEvent) => {
    if (e.target instanceof Element && e.target.closest('a')) return
    if (window.getSelection()?.toString()) return
    navigate(href)
  }

  return (
    <tr className={page.row} onClick={onRowClick}>
      <td>
        <Link to={href} className={styles.title}>
          <strong title={issue.type}>{shortType(issue.type)}</strong>
          {issue.message && issue.message !== issue.type ? ` ${issue.message}` : ''}
        </Link>
        <div className={styles.meta}>
          {issue.isNew && <Tag tone="new">New</Tag>}
          <Tag>{sourceLabel(issue.source)}</Tag>
          {issue.culprit && <code className={styles.culprit}>{issue.culprit}</code>}
        </div>
      </td>
      <td className={styles.barsColumn}>
        <HourBars counts={issue.hourly} variant="bars" />
      </td>
      <td className={`${page.fit} ${styles.events}`}>{issue.events.toLocaleString('en-US')}</td>
      <td className={page.fit}>{issue.users.toLocaleString('en-US')}</td>
      <td className={`${page.fit} ${styles.lastSeen}`}>
        <time dateTime={issue.lastSeen} title={lastSeen.toLocaleString()}>
          {timeAgo(lastSeen, now)}
        </time>
      </td>
    </tr>
  )
}

function EmptyIssues({ list }: { list: IssueList }) {
  const everSeen = list.counts.resolved + list.counts.ignored > 0
  return (
    <p className={page.empty}>
      {everSeen ? (
        'No open issues right now.'
      ) : (
        <>
          No errors yet. Point your app's Sentry SDK at the DSN that <code>monitor project create</code> printed for{' '}
          {list.project.name}.
        </>
      )}
    </p>
  )
}
