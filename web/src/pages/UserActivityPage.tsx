import { useState } from 'react'
import { Link, useParams } from 'react-router'
import { type ActivityItem, ApiError } from '../api/client'
import button from '../components/Button.module.css'
import { ActivityIcon, ErrorMarkIcon, FileIcon } from '../components/Icons'
import { LoadError, OlderData } from '../components/LoadError'
import { ACTIVITY_PAGE_SIZE, activityHeadline, clockTime, dayGroups, loadActivity, propertiesText } from '../lib/activity'
import { shortType } from '../lib/format'
import { formatPageTime } from '../lib/pages'
import { usePolling } from '../lib/usePolling'
import { NotFoundPage } from './NotFoundPage'
import page from './Page.module.css'
import styles from './UserActivityPage.module.css'

// UserActivityPage is everything one user id did in a project, the newest
// first, grouped by day: page views with their time on the page, custom
// events with their properties, and errors linking to their issue. It is
// opened from "See all activity of this user" on an issue. No design; it
// follows the issue page's "Before this error" card (round markers, mono
// pages, times on the right). "Show older" adds the next 50.
export function UserActivityPage() {
  const params = useParams()
  const projectId = Number(params.id)
  const userId = params.userId ?? ''
  const key = `${projectId}|${userId}`

  // Show older opens one more page; another user starts at one page again.
  const [more, setMore] = useState({ key, pages: 1 })
  const pages = more.key === key ? more.pages : 1
  const count = pages * ACTIVITY_PAGE_SIZE
  const polled = usePolling((signal) => loadActivity(projectId, userId, count, signal), `activity-${key}`, String(count))
  const { data, error, stale } = polled

  if (error instanceof ApiError && error.status === 404) return <NotFoundPage what="Project" />
  if (!data) return <section aria-busy={!error}>{error ? <LoadError /> : null}</section>

  const now = new Date()
  const headline = activityHeadline(data, now)
  return (
    <section>
      <nav className={page.breadcrumb} aria-label="Breadcrumb">
        <Link to="/">All projects</Link>
        <span className={page.separator} aria-hidden="true">
          /
        </span>
        <Link to={`/projects/${projectId}/issues`}>{data.project.name} issues</Link>
      </nav>
      <h1 className={page.title}>{headline.title}</h1>
      <p className={page.lead}>{headline.lead}</p>
      {error !== undefined && <OlderData className={styles.notice} />}

      {data.items.length > 0 && (
        <div className={`${page.card} ${styles.card}`} aria-busy={stale}>
          {dayGroups(data.items, now).map((group) => (
            <section key={group.label} className={styles.day} aria-label={group.label}>
              <h2 className={styles.dayTitle}>{group.label}</h2>
              <ol className={styles.items}>
                {group.items.map((item, i) => (
                  <ActivityRow key={`${item.at}-${i}`} item={item} projectId={projectId} />
                ))}
              </ol>
            </section>
          ))}
          {data.next && (
            <footer className={styles.footer}>
              <button type="button" className={`${button.button} ${button.large}`} onClick={() => setMore({ key, pages: pages + 1 })}>
                Show older
              </button>
            </footer>
          )}
        </div>
      )}
    </section>
  )
}

/** ActivityRow is one thing the user did, with the time it happened. */
function ActivityRow({ item, projectId }: { item: ActivityItem; projectId: number }) {
  const at = new Date(item.at)
  return (
    <li className={`${styles.item} ${item.kind === 'error' ? styles.errorItem : ''}`}>
      <time className={styles.time} dateTime={item.at} title={at.toLocaleString()}>
        {clockTime(at)}
      </time>
      <Marker kind={item.kind} />
      <ActivityText item={item} projectId={projectId} />
    </li>
  )
}

function Marker({ kind }: { kind: ActivityItem['kind'] }) {
  if (kind === 'error') {
    return (
      <span className={`${styles.marker} ${styles.errorMarker}`} aria-label="Error">
        <ErrorMarkIcon className={styles.errorMark} />
      </span>
    )
  }
  if (kind === 'event') {
    return (
      <span className={`${styles.marker} ${styles.eventMarker}`} aria-label="Event">
        <ActivityIcon className={styles.icon} />
      </span>
    )
  }
  return (
    <span className={`${styles.marker} ${styles.pageMarker}`} aria-label="Page view">
      <FileIcon className={styles.icon} />
    </span>
  )
}

function ActivityText({ item, projectId }: { item: ActivityItem; projectId: number }) {
  switch (item.kind) {
    case 'pageview':
      return (
        <>
          <span className={styles.main}>
            <span className={styles.path} title={item.route !== item.path ? `Route ${item.route}` : undefined}>
              {item.path}
            </span>
          </span>
          {item.durationMs !== undefined && <span className={styles.side}>{formatPageTime(item.durationMs)}</span>}
        </>
      )
    case 'event': {
      const props = propertiesText(item.properties)
      return (
        <>
          <span className={styles.main}>
            <span className={styles.event}>{item.name}</span>
            {props && <span className={styles.detail}>{props}</span>}
          </span>
          {item.path && <span className={`${styles.side} ${styles.sidePath}`}>{item.path}</span>}
        </>
      )
    }
    case 'error':
      return (
        <>
          <span className={styles.main}>
            <Link to={`/projects/${projectId}/issues/${item.issueId}`} className={styles.error} title={item.type}>
              {shortType(item.type)}
            </Link>
            {item.message && item.message !== item.type && <span className={styles.detail}>{item.message}</span>}
          </span>
          {item.path && <span className={`${styles.side} ${styles.sidePath}`}>{item.path}</span>}
        </>
      )
  }
}
