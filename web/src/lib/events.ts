// Text of the Events screen. Pure functions, tested in events.test.ts.

import type { EventList, PageRange } from '../api/client'
import { RANGES } from './pages'

function rangeLabel(range: PageRange): string {
  return RANGES.find((r) => r.range === range)?.label ?? range
}

function count(n: number): string {
  return n.toLocaleString('en-US')
}

/** timesText writes how often something happened: "once", "2 times", "1,204 times". */
export function timesText(n: number): string {
  return n === 1 ? 'once' : `${count(n)} times`
}

/** selectedEvent is the event the screen shows: the one in the URL, else the most frequent ('' with none). */
export function selectedEvent(list: EventList, fromURL: string | null): string {
  return fromURL || (list.events[0]?.name ?? '')
}

/**
 * eventsHeadline is the conclusion on top of the Events screen, about the
 * selected event: "checkout_completed happened 1,204 times in the last 7
 * days", then by how many users and the project and period.
 */
export function eventsHeadline(list: EventList, selected: string): { title: string; lead: string } {
  const name = list.project.name
  const period = `last ${rangeLabel(list.range)}`
  if (list.events.length === 0) {
    if (list.bots > 0) {
      return {
        title: `Only bots sent events to ${name}`,
        lead: `${count(list.bots)} bot ${list.bots === 1 ? 'event' : 'events'} in the ${period}, not counted.`,
      }
    }
    return {
      title: 'No custom events yet',
      lead: `${name}, ${period}. Events your app sends with posthog.capture() show up here a minute or two later.`,
    }
  }
  // "7 days" stays on one line when the headline wraps.
  const titlePeriod = period.replace(/ (\S+)$/, '\u00a0$1')
  const event = list.events.find((e) => e.name === selected)
  if (!event) return { title: `${selected} did not happen in the ${titlePeriod}`, lead: `${name}, ${period}.` }
  return {
    title: `${event.name} happened ${timesText(event.count)} in the ${titlePeriod}`,
    lead: `By ${count(event.users)} ${event.users === 1 ? 'user' : 'users'}. ${name}, ${period}.`,
  }
}

/** eventSeriesTitle is the chart's title. */
export function eventSeriesTitle(range: PageRange): string {
  return range === '24h' ? 'Times per hour' : 'Times per day'
}

/** botEventsLine is the quiet line under the numbers: "12 bot events not counted"; empty without bots. */
export function botEventsLine(bots: number): string {
  if (bots === 0) return ''
  return `${count(bots)} bot ${bots === 1 ? 'event' : 'events'} not counted`
}
