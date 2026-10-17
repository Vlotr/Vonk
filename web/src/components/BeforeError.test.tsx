import { renderToString } from 'react-dom/server'
import { expect, test } from 'vitest'
import { BeforeError } from './BeforeError'

const steps = [
  { route: '/products/hoodie-classic', durationMs: 100_000, start: '2026-10-16T12:20:00Z' },
  { route: '/cart', durationMs: 35_000, start: '2026-10-16T12:22:00Z' },
  { route: '/checkout', durationMs: null, start: '2026-10-16T12:23:00Z' },
]

const plain = (html: string) => html.replace(/<[^>]+>/g, ' ').replace(/\s+/g, ' ')

test('the pages before the error, numbered and with their time, then the error itself, as the design shows', () => {
  const html = renderToString(<BeforeError steps={steps} errorRoute="/checkout" />)
  expect(plain(html)).toContain(
    'Before this error Pages this user saw, from page tracking. 1 /products/hoodie-classic 1m 40s 2 /cart 35s 3 /checkout Error on /checkout',
  )
  // In order, as a list; the page of the error has no time yet. When each page opened is its tooltip.
  expect(html.match(/<li[ >]/g)).toHaveLength(4)
  expect(html).toContain('title="/products/hoodie-classic, opened at ')
})

test('nothing to show: no card', () => {
  expect(renderToString(<BeforeError steps={[]} errorRoute="/checkout" />)).toBe('')
})

test('an error without a page', () => {
  expect(plain(renderToString(<BeforeError steps={steps.slice(0, 1)} />))).toContain('The error')
})
