import { type MouseEvent, useEffect, useState } from 'react'
import { Link, useNavigate, useParams, useSearchParams } from 'react-router'
import { ApiError, type Issue, type IssueSort, type IssueSourceFilter, type IssueStatus } from '../api/client'
import button from '../components/Button.module.css'
import { HourBars } from '../components/HourBars'
import { ChevronDownIcon } from '../components/Icons'
import { LoadError, OlderData } from '../components/LoadError'
import { Tag } from '../components/Tag'
import {
  emptyIssuesSentence,
  issuesHeadline,
  newIssuesSentence,
  shortType,
  showingSentence,
  sourceLabel,
  timeAgo,
} from '../lib/format'
import { filtersToParams, hasFilters, type IssueFilters, parseIssueFilters } from '../lib/issueFilters'
import { type LoadedIssues, loadIssues, withIssueMoved } from '../lib/issueList'
import { usePolling } from '../lib/usePolling'
import { useStatusChange } from '../lib/useStatusChange'
import styles from './IssuesPage.module.css'
import { NotFoundPage } from './NotFoundPage'
import page from './Page.module.css'

/** PAGE_SIZE is how many issues the list shows at first, and how many more "Show more" adds. */
const PAGE_SIZE = 25

/** SEARCH_DELAY_MS is how long typing has to pause before the search runs. */
const SEARCH_DELAY_MS = 300

const TABS: { status: IssueStatus; label: string }[] = [
  { status: 'open', label: 'Open' },
  { status: 'resolved', label: 'Resolved' },
  { status: 'ignored', label: 'Ignored' },
]

// IssuesPage lists a project's issues: the status tabs, source and order,
// a search, and the issues with a button to resolve (or reopen) each. Tab,
// filters and search live in the URL query.
export function IssuesPage() {
  const projectId = Number(useParams().id)
  const [params, setParams] = useSearchParams()
  const filters = parseIssueFilters(params)
  const filterKey = `${projectId}|${filtersToParams(filters).toString()}`

  // Show more opens one more page; other filters start at one page again.
  const [more, setMore] = useState({ filterKey, pages: 1 })
  const pages = more.filterKey === filterKey ? more.pages : 1
  const count = pages * PAGE_SIZE

  const polled = usePolling(
    (signal) => loadIssues(projectId, filters, count, signal),
    `issues-${projectId}`,
    `${filterKey}|${count}`,
  )
  const status = useStatusChange(polled)

  // Built on the URL as it is when the change lands, not as it was when this
  // render happened: a search that runs 300 ms later must not undo a tab
  // clicked in between.
  const setFilters = (change: Partial<IssueFilters>, replace = false) => {
    status.clearFailed()
    setParams((current) => filtersToParams({ ...parseIssueFilters(current), ...change }, current), { replace })
  }

  // The rows' tab is the one they were loaded for, which differs from the
  // URL's while (or after failing) to load another tab.
  const changeStatus = (issue: Issue, to: IssueStatus) =>
    status.change(issue.id, to, (list) => withIssueMoved(list, issue, list.status, to), shortType(issue.type))

  const { data, error } = polled
  if (error instanceof ApiError && error.status === 404) return <NotFoundPage what="Project" />
  if (!data) return <section aria-busy={!error}>{error ? <LoadError /> : null}</section>

  return (
    <section>
      <nav className={page.breadcrumb} aria-label="Breadcrumb">
        <Link to="/">All projects</Link>
      </nav>
      <h1 className={page.title}>{issuesHeadline(data.openIssues, data.project.name)}</h1>
      <p className={page.lead}>{newIssuesSentence(data.openIssues, data.new24h)}</p>

      <div className={styles.controls}>
        <div className={styles.tabs} role="group" aria-label="Status">
          {TABS.map((tab) => (
            <button
              key={tab.status}
              type="button"
              className={styles.tab}
              aria-pressed={filters.status === tab.status}
              onClick={() => setFilters({ status: tab.status })}
            >
              {tab.label} <span className={styles.count}>{data.counts[tab.status].toLocaleString('en-US')}</span>
            </button>
          ))}
        </div>
        <label className={styles.field}>
          <span className={styles.label}>Source</span>
          <span className={styles.selectBox}>
            <select
              className={`${styles.select} ${styles.sourceSelect}`}
              value={filters.source}
              onChange={(e) => setFilters({ source: e.target.value as IssueSourceFilter })}
            >
              <option value="">All sources</option>
              <option value="laravel">Laravel</option>
              <option value="browser">Browser</option>
            </select>
            <ChevronDownIcon className={styles.chevron} />
          </span>
        </label>
        <label className={styles.field}>
          <span className={styles.label}>Sort by</span>
          <span className={styles.selectBox}>
            <select
              className={`${styles.select} ${styles.sortSelect}`}
              value={filters.sort}
              onChange={(e) => setFilters({ sort: e.target.value as IssueSort })}
            >
              <option value="last_seen">Last seen</option>
              <option value="first_seen">First seen</option>
              <option value="events">Events</option>
              <option value="users">Users</option>
            </select>
            <ChevronDownIcon className={styles.chevron} />
          </span>
        </label>
        <SearchBox value={filters.q} onSearch={(q) => setFilters({ q }, true)} />
      </div>

      {filters.route && (
        <p className={styles.route}>
          Only errors on the page <code>{filters.route}</code>.{' '}
          <button type="button" className={styles.linkButton} onClick={() => setFilters({ route: '' })}>
            Show all pages
          </button>
        </p>
      )}
      {status.failed && (
        <p className={styles.failed} role="alert">
          {status.failed}
        </p>
      )}
      {polled.error !== undefined && <OlderData className={styles.route} />}

      <div className={`${page.card} ${styles.card}`} aria-busy={polled.stale}>
        {data.issues.length > 0 ? (
          <IssueTable
            list={data}
            stale={polled.stale}
            onStatus={changeStatus}
            onShowMore={() => setMore({ filterKey, pages: pages + 1 })}
          />
        ) : (
          <EmptyIssues list={data} filters={filters} />
        )}
      </div>
    </section>
  )
}

/**
 * SearchBox runs the search once typing pauses for 300 ms (Enter runs it
 * at once). It shows what was typed even before the URL has it.
 */
function SearchBox({ value, onSearch }: { value: string; onSearch: (q: string) => void }) {
  const [text, setText] = useState(value)
  const [lastValue, setLastValue] = useState(value)
  if (value !== lastValue) {
    // The URL changed by itself (back button, a link): show its search.
    setLastValue(value)
    setText(value)
  }

  useEffect(() => {
    if (text === value) return
    const timer = setTimeout(() => onSearch(text), SEARCH_DELAY_MS)
    return () => clearTimeout(timer)
    // onSearch is a new closure on every render; text and value say when to run.
  }, [text, value])

  return (
    <label className={`${styles.field} ${styles.search}`}>
      <span className={styles.label}>Search</span>
      <input
        className={styles.input}
        type="search"
        placeholder="Error, file or page"
        maxLength={200}
        value={text}
        onChange={(e) => setText(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === 'Enter' && text !== value) onSearch(text)
        }}
      />
    </label>
  )
}

interface IssueTableProps {
  list: LoadedIssues
  stale: boolean
  onStatus: (issue: Issue, to: IssueStatus) => void
  onShowMore: () => void
}

function IssueTable({ list, stale, onStatus, onShowMore }: IssueTableProps) {
  const now = new Date()
  return (
    <>
      <div className={`${page.scroll} ${stale ? styles.stale : ''}`}>
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
              <th scope="col" className={styles.lastSeenColumn}>
                Last seen
              </th>
              <th scope="col" className={`${page.fit} ${styles.actionColumn}`}>
                <span className={styles.hidden}>Action</span>
              </th>
            </tr>
          </thead>
          <tbody>
            {list.issues.map((issue) => (
              <IssueRow
                key={issue.id}
                issue={issue}
                projectId={list.project.id}
                status={list.status}
                now={now}
                disabled={stale}
                onStatus={onStatus}
              />
            ))}
          </tbody>
        </table>
      </div>
      <div className={styles.footer}>
        <span>{showingSentence(list.issues.length, list.total, list.status)}</span>
        {list.issues.length < list.total && (
          <button type="button" className={`${button.button} ${button.large}`} disabled={stale} onClick={onShowMore}>
            Show more
          </button>
        )}
      </div>
    </>
  )
}

interface IssueRowProps {
  issue: Issue
  projectId: number
  /** The tab: open rows get Resolve, the others Reopen. */
  status: IssueStatus
  now: Date
  /** While rows of the previous tab or filters are on screen, their button waits. */
  disabled: boolean
  onStatus: (issue: Issue, to: IssueStatus) => void
}

function IssueRow({ issue, projectId, status, now, disabled, onStatus }: IssueRowProps) {
  const navigate = useNavigate()
  const href = `/projects/${projectId}/issues/${issue.id}`
  const lastSeen = new Date(issue.lastSeen)

  // The title is the link; a click anywhere else on the row opens the issue too.
  const onRowClick = (e: MouseEvent) => {
    if (e.target instanceof Element && e.target.closest('a, button')) return
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
      <td className={page.fit}>
        <button
          type="button"
          className={button.button}
          disabled={disabled}
          onClick={() => onStatus(issue, status === 'open' ? 'resolved' : 'open')}
        >
          {status === 'open' ? 'Resolve' : 'Reopen'}
        </button>
      </td>
    </tr>
  )
}

function EmptyIssues({ list, filters }: { list: LoadedIssues; filters: IssueFilters }) {
  const everSeen = list.counts.open + list.counts.resolved + list.counts.ignored > 0
  if (!everSeen && !hasFilters(filters)) {
    return (
      <p className={page.empty}>
        No errors yet. Point your app's Sentry SDK at the DSN that <code>monitor project create</code> printed for{' '}
        {list.project.name}.
      </p>
    )
  }
  return <p className={page.empty}>{emptyIssuesSentence(list.status, hasFilters(filters), filters.q.trim())}</p>
}
