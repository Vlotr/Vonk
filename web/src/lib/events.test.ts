import { describe, expect, test } from 'vitest'
import type { EventList } from '../api/client'
import { botEventsLine, eventsHeadline, eventSeriesTitle, selectedEvent, timesText } from './events'

function list(events: EventList['events'], extra: Partial<EventList> = {}): EventList {
  return { project: { id: 1, name: 'Webshop', tracksPageviews: true }, range: '7d', events, bots: 0, ...extra }
}

const shop = list([
  { name: 'checkout_completed', count: 1_204, users: 980 },
  { name: 'product_viewed', count: 812, users: 401 },
  { name: 'newsletter_signup', count: 1, users: 1 },
])

describe('eventsHeadline', () => {
  test('says how often the selected event happened, as the brief words it', () => {
    expect(eventsHeadline(shop, 'checkout_completed')).toEqual({
      title: 'checkout_completed happened 1,204 times in the last 7\u00a0days',
      lead: 'By 980 users. Webshop, last 7 days.',
    })
  })

  test('once, by one user, in another period', () => {
    expect(eventsHeadline({ ...shop, range: '24h' }, 'newsletter_signup')).toEqual({
      title: 'newsletter_signup happened once in the last 24\u00a0hours',
      lead: 'By 1 user. Webshop, last 24 hours.',
    })
  })

  test('an event of the URL that did not happen in the period', () => {
    expect(eventsHeadline(shop, 'refund_requested')).toEqual({
      title: 'refund_requested did not happen in the last 7\u00a0days',
      lead: 'Webshop, last 7 days.',
    })
  })

  test('no events yet, or only from bots', () => {
    expect(eventsHeadline(list([]), '')).toEqual({
      title: 'No custom events yet',
      lead: 'Webshop, last 7 days. Events your app sends with posthog.capture() show up here a minute or two later.',
    })
    expect(eventsHeadline(list([], { bots: 3, range: '30d' }), '')).toEqual({
      title: 'Only bots sent events to Webshop',
      lead: '3 bot events in the last 30 days, not counted.',
    })
  })
})

describe('selectedEvent', () => {
  test('the event of the URL, else the most frequent', () => {
    expect(selectedEvent(shop, 'product_viewed')).toBe('product_viewed')
    expect(selectedEvent(shop, null)).toBe('checkout_completed')
    expect(selectedEvent(list([]), null)).toBe('')
  })
})

test('timesText, eventSeriesTitle and botEventsLine', () => {
  expect(timesText(0)).toBe('0 times')
  expect(timesText(1)).toBe('once')
  expect(timesText(2)).toBe('2 times')
  expect(timesText(12_345)).toBe('12,345 times')
  expect(eventSeriesTitle('7d')).toBe('Times per day')
  expect(eventSeriesTitle('24h')).toBe('Times per hour')
  expect(botEventsLine(0)).toBe('')
  expect(botEventsLine(1)).toBe('1 bot event not counted')
  expect(botEventsLine(1_500)).toBe('1,500 bot events not counted')
})
