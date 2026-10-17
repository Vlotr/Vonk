import { describe, expect, test } from 'vitest'
import type { ActivityItem, UserActivity } from '../api/client'
import { activityHeadline, clockTime, dayGroups, errorStepText, loadActivity, propertiesText, userActivityPath } from './activity'

const now = new Date(2026, 9, 16, 14, 30)

function at(day: number, hours: number, minutes: number): string {
  return new Date(2026, 9, day, hours, minutes).toISOString()
}

function activity(items: ActivityItem[], next: string | null = null): UserActivity {
  return { project: { id: 1, name: 'Webshop' }, userId: '42', items, next }
}

describe('userActivityPath', () => {
  test('encodes the user id, which can hold dots, @ and slashes', () => {
    expect(userActivityPath(1, '42')).toBe('/projects/1/users/42')
    expect(userActivityPath(1, 'anna.de.vries@example.com')).toBe('/projects/1/users/anna.de.vries%40example.com')
    expect(userActivityPath(3, 'team/7 b')).toBe('/projects/3/users/team%2F7%20b')
  })
})

test('clockTime has seconds', () => {
  expect(clockTime(new Date(2026, 9, 16, 9, 5, 7))).toBe('09:05:07')
})

test('errorStepText is the last row of "Before this error"', () => {
  expect(errorStepText('/checkout')).toBe('Error on /checkout')
  expect(errorStepText(undefined)).toBe('The error')
})

describe('activityHeadline', () => {
  test('when the user was last seen', () => {
    const items: ActivityItem[] = [{ kind: 'pageview', at: at(16, 14, 26), path: '/checkout', route: '/checkout' }]
    expect(activityHeadline(activity(items), now)).toEqual({
      title: 'User 42 was last seen 4 min ago',
      lead: 'Their page views, errors and custom events in Webshop, the newest first.',
    })
  })

  test('nothing stored for the user', () => {
    expect(activityHeadline(activity([]), now)).toEqual({
      title: 'Nothing stored for user 42',
      lead: "Webshop has no page views, errors or custom events of this user id. Data older than the project's retention is deleted.",
    })
  })
})

test('dayGroups puts the items under Today, Yesterday and a date, in their order', () => {
  const items: ActivityItem[] = [
    { kind: 'error', at: at(16, 9, 0), issueId: 1, type: 'TypeError', message: 'x' },
    { kind: 'event', at: at(16, 8, 0), name: 'checkout_started' },
    { kind: 'pageview', at: at(15, 23, 59), path: '/', route: '/' },
    { kind: 'pageview', at: at(12, 10, 0), path: '/cart', route: '/cart' },
  ]
  const groups = dayGroups(items, now)
  expect(groups.map((g) => [g.label, g.items.length])).toEqual([
    ['Today', 2],
    ['Yesterday', 1],
    ['Oct 12', 1],
  ])
  expect(groups[0].items[0].kind).toBe('error')
})

test('propertiesText lists a few of the event properties', () => {
  expect(propertiesText(undefined)).toBe('')
  expect(propertiesText({ plan: 'pro', total: 49.95, gift: false })).toBe('plan: pro · total: 49.95 · gift: false')
  expect(propertiesText({ a: 1, b: 2, c: 3, d: 4, e: 5 })).toBe('a: 1 · b: 2 · c: 3 · d: 4 · 1 more')
  expect(propertiesText({ cart: { items: 2 } })).toBe('cart: {"items":2}')
})

describe('loadActivity', () => {
  test('loads pages through next until it has count items, and keeps the last next', async () => {
    const pages: Record<string, UserActivity> = {
      '': activity([{ kind: 'pageview', at: at(16, 14, 0), path: '/a', route: '/a' }], 'c1'),
      c1: activity([{ kind: 'pageview', at: at(16, 13, 0), path: '/b', route: '/b' }], 'c2'),
      c2: activity([{ kind: 'pageview', at: at(16, 12, 0), path: '/c', route: '/c' }], null),
    }
    const asked: string[] = []
    const fake = async (_projectId: number, _userId: string, options: { before?: string; limit?: number } = {}) => {
      asked.push(`${options.before ?? ''}/${options.limit}`)
      return pages[options.before ?? '']
    }
    const two = await loadActivity(1, '42', 2, new AbortController().signal, 1, fake)
    expect(two.items.map((i) => (i.kind === 'pageview' ? i.path : ''))).toEqual(['/a', '/b'])
    expect(two.next).toBe('c2')
    expect(asked).toEqual(['/1', 'c1/1'])

    const all = await loadActivity(1, '42', 10, new AbortController().signal, 1, fake)
    expect(all.items).toHaveLength(3)
    expect(all.next).toBeNull()
  })
})
