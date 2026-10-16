// Text and numbers of the Pages screen. Pure functions, tested in pages.test.ts.

import type { PageRange, PageRow, PageStats } from '../api/client'

const MONTHS = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec']

/** RANGES are the toggle's choices, as the design names them. */
export const RANGES: { range: PageRange; label: string }[] = [
  { range: '24h', label: '24 hours' },
  { range: '7d', label: '7 days' },
  { range: '30d', label: '30 days' },
]

function rangeLabel(range: PageRange): string {
  return RANGES.find((r) => r.range === range)?.label ?? range
}

function count(n: number): string {
  return n.toLocaleString('en-US')
}

/**
 * longestStay is the page where visitors stay longest: the longest average
 * time among the pages with at least 1 % of all views (a page seen twice
 * says little), or among all pages with a time when none has that many.
 * Null when no page has a time.
 */
export function longestStay(pages: PageRow[], totalViews: number): PageRow | null {
  const timed = pages.filter((p) => p.avgTimeMs !== null)
  const enough = timed.filter((p) => p.views * 100 >= totalViews)
  let best: PageRow | null = null
  for (const p of enough.length > 0 ? enough : timed) {
    if (best === null || (p.avgTimeMs ?? 0) > (best.avgTimeMs ?? 0)) best = p
  }
  return best
}

/** mostErrors is the page with the most errors (the busier one on a tie), or null when none had any. */
function mostErrors(pages: PageRow[]): PageRow | null {
  let worst: PageRow | null = null
  for (const p of pages) {
    if (p.errors > 0 && (worst === null || p.errors > worst.errors)) worst = p
  }
  return worst
}

/**
 * pagesHeadline is the conclusion on top of the Pages screen: where
 * visitors stay longest, and whether that page also has the most errors,
 * then the project and the period ("Webshop, last 7 days.").
 */
export function pagesHeadline(stats: PageStats): { title: string; lead: string } {
  const name = stats.project.name
  const period = `${name}, last ${rangeLabel(stats.range)}.`
  if (stats.totals.views === 0) {
    if (stats.bots > 0) {
      return {
        title: `Only bots visited ${name}`,
        lead: `${count(stats.bots)} bot ${stats.bots === 1 ? 'visit' : 'visits'} in the last ${rangeLabel(stats.range)}, not counted as page views.`,
      }
    }
    return {
      title: 'No page views yet',
      lead: stats.project.tracksPageviews
        ? `${period} Page views show up here a minute or two after posthog-js sends them.`
        : `Page views are off for ${name}.`,
    }
  }

  const errors = mostErrors(stats.pages)
  const longest = longestStay(stats.pages, stats.totals.views)
  if (longest) {
    let errorSentence = 'No page had errors.'
    if (errors === longest) errorSentence = 'It is also the page with the most errors.'
    else if (errors) errorSentence = `The most errors happen on ${errors.route}.`
    return { title: `Visitors stay longest on ${longest.route}`, lead: `${errorSentence} ${period}` }
  }

  // Nobody left a page yet, so no page has a time.
  const top = stats.pages[0]
  let errorSentence = 'No page had errors.'
  if (errors === top) errorSentence = `${top.route} also has the most errors.`
  else if (errors) errorSentence = `The most errors happen on ${errors.route}.`
  return {
    title: `${top.route} gets the most views`,
    lead: `Times on a page show up once visitors leave it. ${errorSentence} ${period}`,
  }
}

/** formatPageTime writes a time on a page as the Pages design does: "38s", "1m 21s", and "1h 05m" from an hour. */
export function formatPageTime(ms: number): string {
  const seconds = Math.round(ms / 1000)
  if (seconds < 60) return `${seconds}s`
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m ${String(seconds % 60).padStart(2, '0')}s`
  const minutes = Math.round(seconds / 60)
  return `${Math.floor(minutes / 60)}h ${String(minutes % 60).padStart(2, '0')}m`
}

/** seriesTitle is the chart's title. */
export function seriesTitle(range: PageRange): string {
  return range === '24h' ? 'Page views per hour' : 'Page views per day'
}

/** barLabel names a bar of the chart in local time: "Oct 10" for a day, "14:00" for an hour. */
export function barLabel(start: string, range: PageRange): string {
  const d = new Date(start)
  if (range === '24h') return `${String(d.getHours()).padStart(2, '0')}:00`
  return `${MONTHS[d.getMonth()]} ${d.getDate()}`
}

/** labelEvery says which bars get a label under them: every one of a week, every third hour, every fifth day of a month. */
export function labelEvery(bars: number): number {
  if (bars <= 12) return 1
  return bars <= 24 ? 3 : 5
}

/** botsLine is the quiet line under the numbers: "312 bot visits not counted"; empty without bots. */
export function botsLine(bots: number): string {
  if (bots === 0) return ''
  return `${count(bots)} bot ${bots === 1 ? 'visit' : 'visits'} not counted`
}
