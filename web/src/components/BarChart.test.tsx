import { renderToString } from 'react-dom/server'
import { expect, test } from 'vitest'
import { BarChart } from './BarChart'

const bars = [3, 0, 6].map((value, i) => ({ label: `Oct ${10 + i}`, title: `Oct ${10 + i}: ${value} page views`, value }))

test('bars are as tall as their share of the busiest, and the latest is drawn darker', () => {
  const html = renderToString(<BarChart bars={bars} label="Page views per day" values labelEvery={1} />)
  expect(html).toContain('height:calc(var(--chart-bar-height) * 0.5)')
  expect(html).toContain('height:calc(var(--chart-bar-height) * 0)')
  expect(html).toContain('height:calc(var(--chart-bar-height) * 1)')
  expect(html.match(/latest/g)).toHaveLength(1)
  expect(html.lastIndexOf('latest')).toBeGreaterThan(html.indexOf('* 0.5'))
  expect(html).toContain('Oct 12: 6 page views')
})

test('many bars get no numbers on top, and labels only now and then', () => {
  const many = Array.from({ length: 24 }, (_, i) => ({ label: `${i}:00`, title: `${i}:00`, value: i }))
  const html = renderToString(<BarChart bars={many} label="Page views per hour" values={false} labelEvery={3} />)
  // Labels for the last bar and every third before it: 23, 20, ... 2.
  expect(html).toContain('>23:00<')
  expect(html).toContain('>20:00<')
  expect(html).not.toContain('>22:00</span>')
})
