import { Link, useParams, useSearchParams } from 'react-router'
import { ApiError, getPages, type PageRange, type PageRow, type PageStats } from '../api/client'
import { BarChart } from '../components/BarChart'
import { LoadError } from '../components/LoadError'
import { TimeBar } from '../components/TimeBar'
import { useMe } from '../layout/MeContext'
import { barLabel, botsLine, formatPageTime, labelEvery, pagesHeadline, RANGES, seriesTitle } from '../lib/pages'
import { usePolling } from '../lib/usePolling'
import { NotFoundPage } from './NotFoundPage'
import page from './Page.module.css'
import styles from './PagesPage.module.css'

/** parseRange reads ?range=; 7 days, as the design shows, when it is missing or unknown. */
function parseRange(value: string | null): PageRange {
  return value === '24h' || value === '30d' ? value : '7d'
}

// PagesPage is Pages.png: a headline drawn from the data (where visitors
// stay longest, and whether that page also has the most errors), the
// period toggle, the totals with page views per day or hour, and the
// busiest pages with their time on the page and their errors, which link
// to the issue list of that page. Bots are counted apart, in one quiet
// line. The period is in the URL (?range=24h|7d|30d).
export function PagesPage() {
  const projectId = Number(useParams().id)
  const [params, setParams] = useSearchParams()
  const range = parseRange(params.get('range'))
  const polled = usePolling((signal) => getPages(projectId, range, signal), `pages-${projectId}`, range)
  const { data, error, stale } = polled

  if (error instanceof ApiError && error.status === 404) return <NotFoundPage what="Project" />
  if (!data) return <section aria-busy={!error}>{error ? <LoadError /> : null}</section>

  const headline = pagesHeadline(data)
  const empty = data.totals.views === 0 && data.bots === 0
  return (
    <section>
      <nav className={page.breadcrumb} aria-label="Breadcrumb">
        <Link to="/">All projects</Link>
      </nav>
      <header className={styles.header}>
        <div className={styles.heading}>
          <h1 className={page.title}>{headline.title}</h1>
          <p className={page.lead}>{headline.lead}</p>
        </div>
        <div className={styles.tabs} role="group" aria-label="Period">
          {RANGES.map((r) => (
            <button
              key={r.range}
              type="button"
              className={styles.tab}
              aria-pressed={r.range === range}
              onClick={() => setParams(r.range === '7d' ? {} : { range: r.range }, { replace: true })}
            >
              {r.label}
            </button>
          ))}
        </div>
      </header>

      <div className={stale ? styles.stale : undefined}>
        {empty ? <EmptyCard stats={data} /> : <TotalsCard stats={data} />}
        {!empty && <PagesCard stats={data} />}
      </div>
    </section>
  )
}

function TotalsCard({ stats }: { stats: PageStats }) {
  const bots = botsLine(stats.bots)
  const bars = stats.series.map((point) => {
    const label = barLabel(point.start, stats.range)
    return { label, value: point.views, title: `${label}: ${point.views.toLocaleString('en-US')} ${point.views === 1 ? 'page view' : 'page views'}` }
  })
  return (
    <section className={`${page.card} ${styles.totals}`} aria-label="Totals">
      <div className={styles.numbers}>
        <dl className={styles.stats}>
          <div>
            <dt>Page views</dt>
            <dd>{stats.totals.views.toLocaleString('en-US')}</dd>
          </div>
          <div>
            <dt>Unique visitors</dt>
            <dd>{stats.totals.visitors.toLocaleString('en-US')}</dd>
          </div>
          <div>
            <dt>Avg. time on page</dt>
            <dd>
              {stats.totals.avgTimeMs === null ? (
                <span className={styles.noTime}>Nobody left a page yet</span>
              ) : (
                formatPageTime(stats.totals.avgTimeMs)
              )}
            </dd>
          </div>
        </dl>
        {bots && <p className={styles.bots}>{bots}</p>}
      </div>
      <div className={styles.chart}>
        <h2 className={styles.chartTitle}>{seriesTitle(stats.range)}</h2>
        <BarChart bars={bars} label={seriesTitle(stats.range)} values={bars.length <= 12} labelEvery={labelEvery(bars.length)} />
      </div>
    </section>
  )
}

function PagesCard({ stats }: { stats: PageStats }) {
  const longest = Math.max(0, ...stats.pages.map((p) => p.avgTimeMs ?? 0))
  return (
    <section className={`${page.card} ${styles.pagesCard}`} aria-labelledby="pages-title">
      <header className={styles.cardHeader}>
        <h2 id="pages-title" className={page.cardTitle}>
          Pages
        </h2>
        <span className={styles.sorted}>Sorted by views</span>
      </header>
      <div className={page.scroll}>
        <table className={`${page.table} ${styles.table}`}>
          <thead>
            <tr>
              <th scope="col">Page</th>
              <th scope="col" className={styles.viewsColumn}>
                Views
              </th>
              <th scope="col" className={styles.visitorsColumn}>
                Visitors
              </th>
              <th scope="col" className={styles.timeColumn}>
                Avg. time on page
              </th>
              <th scope="col" className={styles.errorsColumn}>
                Errors
              </th>
            </tr>
          </thead>
          <tbody>
            {stats.pages.map((p) => (
              <PageTableRow key={p.route} row={p} projectId={stats.project.id} longestMs={longest} />
            ))}
          </tbody>
        </table>
      </div>
    </section>
  )
}

function PageTableRow({ row, projectId, longestMs }: { row: PageRow; projectId: number; longestMs: number }) {
  return (
    <tr>
      <td className={styles.route}>{row.route}</td>
      <td className={styles.views}>{row.views.toLocaleString('en-US')}</td>
      <td>{row.visitors.toLocaleString('en-US')}</td>
      <td className={styles.timeCell}>
        <TimeBar ms={row.avgTimeMs} maxMs={longestMs} />
      </td>
      <td>
        {row.errors > 0 ? (
          <Link
            to={`/projects/${projectId}/issues?route=${encodeURIComponent(row.route)}`}
            className={styles.errors}
            aria-label={`${row.errors.toLocaleString('en-US')} ${row.errors === 1 ? 'error' : 'errors'} on ${row.route}: see the issues`}
          >
            {row.errors.toLocaleString('en-US')}
          </Link>
        ) : (
          <span className={styles.noErrors}>0</span>
        )}
      </td>
    </tr>
  )
}

/** EmptyCard says what to do while no page view came in. */
function EmptyCard({ stats }: { stats: PageStats }) {
  const isAdmin = useMe()?.role === 'admin'
  const setup = `/settings/projects/${stats.project.id}/setup#page-views`
  return (
    <section className={page.card} aria-label="Pages">
      <p className={page.empty}>
        {!isAdmin ? (
          'An admin finds the setup steps for posthog-js under Settings, Setup and keys.'
        ) : stats.project.tracksPageviews ? (
          <>
            Add posthog-js to your site with the snippet on <Link to={setup}>Setup and keys</Link>. Each page then shows here with its views,
            visitors and time on the page.
          </>
        ) : (
          <>
            <Link to={setup}>Turn on page views</Link> for {stats.project.name} and add posthog-js to your site with the snippet there.
          </>
        )}
      </p>
    </section>
  )
}
