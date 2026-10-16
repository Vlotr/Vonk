import { type ReactNode, useEffect, useState } from 'react'
import { Link, useParams } from 'react-router'
import { ApiError, type EventDetail, getIssue, type IssueStatus } from '../api/client'
import button from '../components/Button.module.css'
import { HourBars } from '../components/HourBars'
import { CheckIcon, LinkIcon } from '../components/Icons'
import { LoadError } from '../components/LoadError'
import { StackTrace } from '../components/StackTrace'
import { Tag } from '../components/Tag'
import { useProjects } from '../layout/ProjectsContext'
import {
  countryName,
  formatDateTime,
  majorMinor,
  runtimeLabel,
  shortType,
  sourceLabel,
  timeAgo,
  urlPath,
} from '../lib/format'
import { copyText } from '../lib/clipboard'
import { usePolling } from '../lib/usePolling'
import { useStatusChange } from '../lib/useStatusChange'
import styles from './IssueDetailPage.module.css'
import { NotFoundPage } from './NotFoundPage'
import page from './Page.module.css'

// IssueDetailPage shows one issue: its numbers, events per hour, the stack
// trace of its newest event, and who and where it happened, with buttons to
// resolve, ignore or reopen it and to copy its link. Fields the event did
// not carry are left out.
export function IssueDetailPage() {
  const params = useParams()
  const projectId = Number(params.id)
  const issueId = Number(params.issueId)
  const polled = usePolling((signal) => getIssue(issueId, signal), `issue-${issueId}`)
  const { data, error } = polled
  const projects = useProjects()
  const project = projects.data?.find((p) => p.id === projectId)
  const status = useStatusChange(polled)

  // The new status shows at once; the next load brings what the server says.
  const changeStatus = (to: IssueStatus) =>
    status.change(issueId, to, (d) => ({ ...d, issue: { ...d.issue, status: to } }), 'The issue')

  const notFound = (error instanceof ApiError && error.status === 404) || (data && data.issue.projectId !== projectId)
  if (notFound) return <NotFoundPage what="Issue" />
  if (!data) return <section aria-busy={!error}>{error ? <LoadError /> : null}</section>

  const { issue, latestEvent: event } = data
  const now = new Date()
  const message = issue.message && issue.message !== issue.type ? issue.message : ''

  return (
    <section>
      <nav className={page.breadcrumb} aria-label="Breadcrumb">
        <Link to="/">All projects</Link>
        <span className={page.separator} aria-hidden="true">
          /
        </span>
        <Link to={`/projects/${projectId}/issues`}>{project ? `${project.name} issues` : 'Issues'}</Link>
      </nav>

      <header className={styles.header}>
        <div className={styles.heading}>
          <h1 className={page.title} title={issue.type}>
            {shortType(issue.type)}
          </h1>
          {message && (
            <p className={styles.message} title={message}>
              {message}
            </p>
          )}
          <div className={styles.tags}>
            {issue.status !== 'open' && <Tag outline>{issue.status === 'resolved' ? 'Resolved' : 'Ignored'}</Tag>}
            {issue.isNew && <Tag tone="new">New</Tag>}
            <Tag outline>{sourceLabel(issue.source)}</Tag>
            {event?.environment && <Tag outline>{event.environment}</Tag>}
          </div>
        </div>
        <StatusActions status={issue.status} onChange={changeStatus} path={`/projects/${projectId}/issues/${issueId}`} />
      </header>
      {status.failed && (
        <p className={styles.failed} role="alert">
          {status.failed}
        </p>
      )}

      <div className={`${page.card} ${styles.stats}`}>
        <dl className={styles.numbers}>
          <Stat label="Events">{issue.events.toLocaleString('en-US')}</Stat>
          <Stat label="Users affected">{issue.users.toLocaleString('en-US')}</Stat>
          <Stat label="First seen">
            <time dateTime={issue.firstSeen}>{formatDateTime(new Date(issue.firstSeen), now)}</time>
          </Stat>
          <Stat label="Last seen">
            <time dateTime={issue.lastSeen} title={new Date(issue.lastSeen).toLocaleString()}>
              {timeAgo(new Date(issue.lastSeen), now)}
            </time>
          </Stat>
          {event?.release && (
            <Stat label="Release" title={event.release}>
              {event.release}
            </Stat>
          )}
        </dl>
        <div className={styles.hourlyLabel}>Events per hour</div>
        <HourBars counts={data.hourly} variant="blocks" />
        <div className={styles.axis} aria-hidden="true">
          <span>24 h ago</span>
          <span>12 h ago</span>
          <span>Now</span>
        </div>
      </div>

      <div className={styles.columns}>
        <div className={styles.main}>
          {event ? (
            <StackTrace key={issue.id} event={event} />
          ) : (
            <div className={page.card}>
              <p className={page.empty}>The events of this issue are no longer stored.</p>
            </div>
          )}
        </div>
        {event && (
          <div className={styles.side}>
            <UserCard event={event} />
            <DetailsCard event={event} />
          </div>
        )}
      </div>
    </section>
  )
}

/**
 * StatusActions are the buttons at the top right: Resolve and Ignore for an
 * open issue, Reopen and the other one for a resolved or ignored issue, and
 * the copy-link button.
 */
function StatusActions({ status, onChange, path }: { status: IssueStatus; onChange: (to: IssueStatus) => void; path: string }) {
  const large = `${button.button} ${button.large}`
  const choice = (to: IssueStatus, label: string, primary = false) => (
    <button type="button" className={primary ? `${large} ${button.primary}` : large} onClick={() => onChange(to)}>
      {label}
    </button>
  )
  return (
    <div className={styles.actions}>
      {status === 'open' && choice('resolved', 'Resolve', true)}
      {status !== 'open' && choice('open', 'Reopen', true)}
      {status === 'ignored' && choice('resolved', 'Resolve')}
      {status !== 'ignored' && choice('ignored', 'Ignore')}
      <CopyLinkButton path={path} />
    </div>
  )
}

/** COPIED_MS is how long the copy-link button shows its tick. */
const COPIED_MS = 2000

/** CopyLinkButton copies the address of this issue's page. */
function CopyLinkButton({ path }: { path: string }) {
  const [copied, setCopied] = useState(false)
  const [failed, setFailed] = useState(false)
  useEffect(() => {
    if (!copied) return
    const timer = setTimeout(() => setCopied(false), COPIED_MS)
    return () => clearTimeout(timer)
  }, [copied])

  // The tick shows only when the browser says the copy worked.
  const copy = async () => {
    const ok = await copyText(new URL(path, window.location.origin).href)
    setCopied(ok)
    setFailed(!ok)
  }

  const label = copied ? 'Link copied' : failed ? 'The link could not be copied' : 'Copy link'
  return (
    <button type="button" className={`${button.button} ${button.large} ${button.icon}`} onClick={copy} aria-label={label} title={label}>
      {copied ? <CheckIcon /> : <LinkIcon />}
    </button>
  )
}

function Stat({ label, children, title }: { label: string; children: ReactNode; title?: string }) {
  return (
    <div className={styles.stat}>
      <dt>{label}</dt>
      <dd title={title}>{children}</dd>
    </div>
  )
}

interface Field {
  label: string
  value: string
  mono?: boolean
  title?: string
}

// FieldsCard is a card of label and value rows; without rows it is not shown.
function FieldsCard({ title, fields }: { title: string; fields: (Field | false | '' | undefined)[] }) {
  const rows = fields.filter((f): f is Field => Boolean(f))
  if (rows.length === 0) return null
  return (
    <section className={`${page.card} ${styles.fieldsCard}`}>
      <h2 className={page.cardTitle}>{title}</h2>
      <dl className={styles.fields}>
        {rows.map((f) => (
          <div key={f.label} className={styles.field}>
            <dt>{f.label}</dt>
            <dd className={f.mono ? styles.mono : undefined} title={f.title}>
              {f.value}
            </dd>
          </div>
        ))}
      </dl>
    </section>
  )
}

function UserCard({ event }: { event: EventDetail }) {
  return (
    <FieldsCard
      title="User"
      fields={[
        event.user && { label: 'User ID', value: event.user.id, mono: true },
        event.browser && { label: 'Browser', value: event.browser },
        event.os && { label: 'System', value: event.os },
        event.country && { label: 'Country', value: countryName(event.country) },
      ]}
    />
  )
}

function DetailsCard({ event }: { event: EventDetail }) {
  const runtime = event.runtime
  return (
    <FieldsCard
      title="Details"
      fields={[
        event.environment && { label: 'Environment', value: event.environment },
        event.server && { label: 'Server', value: event.server },
        runtime && {
          label: runtime.name ? runtimeLabel(runtime.name) : 'Runtime',
          value: runtime.version ? majorMinor(runtime.version) : runtime.name,
          title: runtime.version || undefined,
        },
        event.url && { label: 'URL', value: urlPath(event.url), mono: true, title: event.url },
      ]}
    />
  )
}
