import { Link, useParams, useSearchParams } from 'react-router'
import { ApiError, type EventList, type EventSeries, getEvents, getEventSeries } from '../api/client'
import { BarChart } from '../components/BarChart'
import { LoadError } from '../components/LoadError'
import { PeriodTabs, parseRange } from '../components/PeriodTabs'
import { useMe } from '../layout/MeContext'
import { botEventsLine, eventSeriesTitle, eventsHeadline, selectedEvent, timesText } from '../lib/events'
import { barLabel, labelEvery } from '../lib/pages'
import { usePolling } from '../lib/usePolling'
import styles from './EventsPage.module.css'
import { NotFoundPage } from './NotFoundPage'
import page from './Page.module.css'
import screen from './StatsPage.module.css'

// EventsPage shows the app's own PostHog events (posthog.capture), in the
// language of the Pages screen: a headline drawn from the data about the
// chosen event ("checkout_completed happened 1,204 times in the last 7
// days"), the period toggle, the event's numbers with a bar per day (or
// hour), and every event of the period with how often it happened and by
// how many users. The most frequent is chosen until another is clicked.
// Period and event are in the URL (?range=24h&event=checkout_completed).
export function EventsPage() {
  const projectId = Number(useParams().id)
  const [params, setParams] = useSearchParams()
  const range = parseRange(params.get('range'))
  const listed = usePolling((signal) => getEvents(projectId, range, signal), `events-${projectId}`, range)
  const list = listed.data
  const selected = list ? selectedEvent(list, params.get('event')) : ''
  const series = usePolling(
    (signal) => (selected ? getEventSeries(projectId, selected, range, signal) : Promise.resolve(null)),
    `event-series-${projectId}`,
    `${range}|${selected}`,
  )

  // Built on the URL as it is, so choosing an event keeps the period and the other way round.
  const change = (key: 'range' | 'event', value: string) =>
    setParams(
      (current) => {
        const next = new URLSearchParams(current)
        if (value) next.set(key, value)
        else next.delete(key)
        return next
      },
      { replace: true },
    )

  if (listed.error instanceof ApiError && listed.error.status === 404) return <NotFoundPage what="Project" />
  if (!list) return <section aria-busy={!listed.error}>{listed.error ? <LoadError /> : null}</section>

  const headline = eventsHeadline(list, selected)
  const empty = list.events.length === 0
  const chart = series.data?.name === selected ? series.data : undefined
  return (
    <section>
      <nav className={page.breadcrumb} aria-label="Breadcrumb">
        <Link to="/">All projects</Link>
      </nav>
      <header className={screen.header}>
        <div className={screen.heading}>
          <h1 className={page.title}>{headline.title}</h1>
          <p className={page.lead}>{headline.lead}</p>
        </div>
        <PeriodTabs range={range} onChange={(r) => change('range', r === '7d' ? '' : r)} />
      </header>

      <div className={listed.stale ? screen.stale : undefined}>
        {empty ? (
          <EmptyCard list={list} />
        ) : (
          <>
            <EventCard list={list} selected={selected} series={chart} />
            <EventsCard list={list} selected={selected} onSelect={(name) => change('event', name)} />
          </>
        )}
      </div>
    </section>
  )
}

/** EventCard is the chosen event's numbers and its bars, as the Pages totals card. */
function EventCard({ list, selected, series }: { list: EventList; selected: string; series?: EventSeries }) {
  const row = list.events.find((e) => e.name === selected)
  const count = series?.count ?? row?.count ?? 0
  const users = series?.users ?? row?.users ?? 0
  const bots = botEventsLine(list.bots)
  const bars = (series?.series ?? []).map((point) => {
    const label = barLabel(point.start, list.range)
    return { label, value: point.count, title: `${label}: ${timesText(point.count)}` }
  })
  return (
    <section className={`${page.card} ${screen.totals}`} aria-label={selected}>
      <div className={screen.numbers}>
        <dl className={screen.stats}>
          <div>
            <dt>Event</dt>
            <dd className={styles.name} title={selected}>
              {selected}
            </dd>
          </div>
          <div>
            <dt>Times</dt>
            <dd>{count.toLocaleString('en-US')}</dd>
          </div>
          <div>
            <dt>Users</dt>
            <dd>{users.toLocaleString('en-US')}</dd>
          </div>
        </dl>
        {bots && <p className={screen.bots}>{bots}</p>}
      </div>
      <div className={screen.chart} aria-busy={!series}>
        <h2 className={screen.chartTitle}>{eventSeriesTitle(list.range)}</h2>
        {series && (
          <BarChart bars={bars} label={`${selected}: ${eventSeriesTitle(list.range)}`} values={bars.length <= 12} labelEvery={labelEvery(bars.length)} />
        )}
      </div>
    </section>
  )
}

/** EventsCard lists every event of the period; clicking one shows it above. */
function EventsCard({ list, selected, onSelect }: { list: EventList; selected: string; onSelect: (name: string) => void }) {
  return (
    <section className={`${page.card} ${screen.listCard}`} aria-labelledby="events-title">
      <header className={screen.cardHeader}>
        <h2 id="events-title" className={page.cardTitle}>
          Events
        </h2>
        <span className={screen.sorted}>Sorted by times</span>
      </header>
      <div className={page.scroll}>
        <table className={`${page.table} ${screen.table}`}>
          <thead>
            <tr>
              <th scope="col">Event</th>
              <th scope="col" className={styles.countColumn}>
                Times
              </th>
              <th scope="col" className={styles.usersColumn}>
                Users
              </th>
            </tr>
          </thead>
          <tbody>
            {list.events.map((e) => (
              <tr key={e.name} className={styles.row} aria-current={e.name === selected ? 'true' : undefined}>
                <td>
                  <button type="button" className={styles.select} onClick={() => onSelect(e.name)}>
                    {e.name}
                  </button>
                </td>
                <td className={screen.count}>{e.count.toLocaleString('en-US')}</td>
                <td>{e.users.toLocaleString('en-US')}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </section>
  )
}

/** EmptyCard says how events get here while none came in. */
function EmptyCard({ list }: { list: EventList }) {
  const isAdmin = useMe()?.role === 'admin'
  const setup = `/settings/projects/${list.project.id}/setup#page-views`
  return (
    <section className={page.card} aria-label="Events">
      <p className={page.empty}>
        {!isAdmin ? (
          'An admin finds the setup steps for posthog-js under Settings, Setup and keys.'
        ) : list.project.tracksPageviews ? (
          <>
            Once posthog-js is on your site (see <Link to={setup}>Setup and keys</Link>), send your own events with{' '}
            <code>posthog.capture('checkout_completed')</code>. Each shows here with how often it happened and by how many users.
          </>
        ) : (
          <>
            <Link to={setup}>Turn on page views</Link> for {list.project.name} to get the posthog-js setup; events you send with{' '}
            <code>posthog.capture('checkout_completed')</code> then show here.
          </>
        )}
      </p>
    </section>
  )
}
