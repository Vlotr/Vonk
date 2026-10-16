import { describe, expect, test } from 'vitest'
import { DEFAULT_FILTERS, filtersToParams, hasFilters, parseIssueFilters } from './issueFilters'

const parse = (query: string) => parseIssueFilters(new URLSearchParams(query))

describe('parseIssueFilters', () => {
  test('an empty query is the default list: open, all sources, last seen', () => {
    expect(parse('')).toEqual({ status: 'open', source: '', sort: 'last_seen', q: '', route: '' })
    expect(parse('')).toEqual(DEFAULT_FILTERS)
  })

  test('reads every filter', () => {
    expect(parse('status=resolved&source=browser&sort=events&q=cart.js&route=%2Fcheckout')).toEqual({
      status: 'resolved',
      source: 'browser',
      sort: 'events',
      q: 'cart.js',
      route: '/checkout',
    })
  })

  test('unknown values fall back to the default instead of failing the page', () => {
    expect(parse('status=deleted&source=python&sort=title')).toEqual(DEFAULT_FILTERS)
  })

  test('keeps the search as typed, spaces included', () => {
    expect(parse('q=cart+').q).toBe('cart ')
  })
})

describe('filtersToParams', () => {
  test('leaves defaults out, so the plain list has a plain URL', () => {
    expect(filtersToParams(DEFAULT_FILTERS).toString()).toBe('')
    expect(filtersToParams({ ...DEFAULT_FILTERS, sort: 'users', q: 'cart js' }).toString()).toBe('sort=users&q=cart+js')
  })

  test('round-trips', () => {
    const filters = { status: 'ignored', source: 'laravel', sort: 'first_seen', q: '/api/stock', route: '/products/{slug}' } as const
    expect(parseIssueFilters(filtersToParams(filters))).toEqual(filters)
  })

  test('keeps other parameters of the URL', () => {
    const params = filtersToParams({ ...DEFAULT_FILTERS, status: 'resolved' }, new URLSearchParams('utm=mail&status=open'))
    expect(params.toString()).toBe('utm=mail&status=resolved')
  })
})

describe('hasFilters', () => {
  test('source, search and route filter; status and sort do not', () => {
    expect(hasFilters(DEFAULT_FILTERS)).toBe(false)
    expect(hasFilters({ ...DEFAULT_FILTERS, status: 'resolved', sort: 'events' })).toBe(false)
    expect(hasFilters({ ...DEFAULT_FILTERS, source: 'browser' })).toBe(true)
    expect(hasFilters({ ...DEFAULT_FILTERS, q: 'x' })).toBe(true)
    expect(hasFilters({ ...DEFAULT_FILTERS, q: '   ' })).toBe(false)
    expect(hasFilters({ ...DEFAULT_FILTERS, route: '/cart' })).toBe(true)
  })
})
