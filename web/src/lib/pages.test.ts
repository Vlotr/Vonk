import { describe, expect, test } from 'vitest'
import type { PageRow, PageStats } from '../api/client'
import { barLabel, botsLine, formatPageTime, labelEvery, longestStay, pagesHeadline, seriesTitle } from './pages'

function stats(pages: Partial<PageRow>[], extra: Partial<PageStats> = {}): PageStats {
  const rows = pages.map((p) => ({ route: '/', views: 0, visitors: 0, avgTimeMs: null, errors: 0, ...p }))
  const views = rows.reduce((sum, p) => sum + p.views, 0)
  return {
    project: { id: 1, name: 'Webshop', tracksPageviews: true },
    range: '7d',
    totals: { views, visitors: views, avgTimeMs: null },
    series: [],
    pages: rows,
    bots: 0,
    ...extra,
  }
}

// The design's table: /checkout has the longest time and the most errors.
const design = stats([
  { route: '/', views: 32_410, avgTimeMs: 38_000, errors: 0 },
  { route: '/products/:slug', views: 28_120, avgTimeMs: 81_000, errors: 241 },
  { route: '/products', views: 21_880, avgTimeMs: 62_000, errors: 18 },
  { route: '/cart', views: 9_640, avgTimeMs: 52_000, errors: 312 },
  { route: '/search', views: 7_890, avgTimeMs: 41_000, errors: 0 },
  { route: '/checkout', views: 6_920, avgTimeMs: 134_000, errors: 1_204 },
  { route: '/account/orders', views: 3_150, avgTimeMs: 105_000, errors: 0 },
])

describe('pagesHeadline', () => {
  test('says where visitors stay longest, and that it also has the most errors, as the design does', () => {
    expect(pagesHeadline(design)).toEqual({
      title: 'Visitors stay longest on /checkout',
      lead: 'It is also the page with the most errors. Webshop, last 7 days.',
    })
  })

  test('names the page with the most errors when it is another one', () => {
    const s = stats([
      { route: '/checkout', views: 100, avgTimeMs: 120_000, errors: 1 },
      { route: '/cart', views: 200, avgTimeMs: 30_000, errors: 9 },
    ])
    expect(pagesHeadline({ ...s, range: '24h' })).toEqual({
      title: 'Visitors stay longest on /checkout',
      lead: 'The most errors happen on /cart. Webshop, last 24 hours.',
    })
  })

  test('says so when no page had errors', () => {
    const s = stats([{ route: '/', views: 10, avgTimeMs: 5_000 }], { range: '30d' })
    expect(pagesHeadline(s).lead).toBe('No page had errors. Webshop, last 30 days.')
  })

  test('without any time on a page, names the busiest page', () => {
    const s = stats([
      { route: '/', views: 10, errors: 2 },
      { route: '/cart', views: 3 },
    ])
    expect(pagesHeadline(s)).toEqual({
      title: '/ gets the most views',
      lead: 'Times on a page show up once visitors leave it. / also has the most errors. Webshop, last 7 days.',
    })
  })

  test('no page views yet', () => {
    expect(pagesHeadline(stats([]))).toEqual({
      title: 'No page views yet',
      lead: 'Webshop, last 7 days. Page views show up here a minute or two after posthog-js sends them.',
    })
    const off = stats([], { project: { id: 1, name: 'Webshop', tracksPageviews: false } })
    expect(pagesHeadline(off)).toEqual({ title: 'No page views yet', lead: 'Page views are off for Webshop.' })
  })

  test('only bots', () => {
    const s = stats([{ route: '/', views: 0 }], { bots: 312 })
    expect(pagesHeadline(s)).toEqual({
      title: 'Only bots visited Webshop',
      lead: '312 bot visits in the last 7 days, not counted as page views.',
    })
  })
})

describe('longestStay', () => {
  test('leaves out pages with too few views to say much: under 1 % of all views', () => {
    const s = stats([
      { route: '/', views: 990, avgTimeMs: 30_000 },
      { route: '/terms', views: 5, avgTimeMs: 600_000 }, // 0.5 %
      { route: '/cart', views: 20, avgTimeMs: 90_000 },
    ])
    expect(longestStay(s.pages, s.totals.views)?.route).toBe('/cart')
  })

  test('takes any page with a time when none has enough views, and none without times', () => {
    const s = stats([
      { route: '/', views: 1000 },
      { route: '/terms', views: 2, avgTimeMs: 60_000 },
    ])
    expect(longestStay(s.pages, s.totals.views)?.route).toBe('/terms')
    expect(longestStay(stats([{ route: '/', views: 4 }]).pages, 4)).toBeNull()
  })
})

describe('formatPageTime', () => {
  test('writes seconds under a minute and minutes with two-digit seconds, as the Pages design does', () => {
    expect(formatPageTime(38_000)).toBe('38s')
    expect(formatPageTime(81_000)).toBe('1m 21s')
    expect(formatPageTime(62_000)).toBe('1m 02s')
    expect(formatPageTime(134_400)).toBe('2m 14s')
    expect(formatPageTime(400)).toBe('0s')
    expect(formatPageTime(59_600)).toBe('1m 00s')
    expect(formatPageTime(3_900_000)).toBe('1h 05m')
  })
})

describe('the chart', () => {
  test('labels days as "Oct 10" and hours as "14:00", in local time', () => {
    expect(barLabel(new Date(2026, 9, 10).toISOString(), '7d')).toBe('Oct 10')
    expect(barLabel(new Date(2026, 9, 16, 14).toISOString(), '24h')).toBe('14:00')
  })

  test('labels every bar of a week, and fewer of a day or a month', () => {
    expect(labelEvery(7)).toBe(1)
    expect(labelEvery(24)).toBe(3)
    expect(labelEvery(30)).toBe(5)
  })

  test('titles', () => {
    expect(seriesTitle('24h')).toBe('Page views per hour')
    expect(seriesTitle('7d')).toBe('Page views per day')
  })
})

test('botsLine counts the bots that are left out', () => {
  expect(botsLine(312)).toBe('312 bot visits not counted')
  expect(botsLine(1)).toBe('1 bot visit not counted')
  expect(botsLine(1_204)).toBe('1,204 bot visits not counted')
  expect(botsLine(0)).toBe('')
})
