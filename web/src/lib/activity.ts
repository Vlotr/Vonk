// One person's trail: the user activity page and the last row of "Before
// this error". Pure functions (and loadActivity with an injectable API
// call), tested in activity.test.ts.

import { type ActivityItem, getUserActivity, type UserActivity } from '../api/client'
import { formatDate, timeAgo } from './format'

/** ACTIVITY_PAGE_SIZE is how many items the activity page shows at first, and how many more "Show older" adds. */
export const ACTIVITY_PAGE_SIZE = 50

/** userActivityPath is the dashboard address of a user's activity; the id is encoded (it may hold dots, @ or slashes). */
export function userActivityPath(projectId: number, userId: string): string {
  return `/projects/${projectId}/users/${encodeURIComponent(userId)}`
}

/** clockTime writes the local time with seconds, "14:32:05": a person's steps can be seconds apart. */
export function clockTime(date: Date): string {
  return [date.getHours(), date.getMinutes(), date.getSeconds()].map((n) => String(n).padStart(2, '0')).join(':')
}

/** errorStepText is the last row of "Before this error": the error and its page. */
export function errorStepText(route: string | undefined): string {
  return route ? `Error on ${route}` : 'The error'
}

/** activityHeadline says when the user was last seen, or that nothing of them is stored. */
export function activityHeadline(activity: UserActivity, now: Date): { title: string; lead: string } {
  const name = activity.project.name
  const latest = activity.items[0]
  if (!latest) {
    return {
      title: `Nothing stored for user ${activity.userId}`,
      lead: `${name} has no page views, errors or custom events of this user id. Data older than the project's retention is deleted.`,
    }
  }
  return {
    title: `User ${activity.userId} was last seen ${timeAgo(new Date(latest.at), now)}`,
    lead: `Their page views, errors and custom events in ${name}, the newest first.`,
  }
}

function sameDay(a: Date, b: Date): boolean {
  return a.getFullYear() === b.getFullYear() && a.getMonth() === b.getMonth() && a.getDate() === b.getDate()
}

/** dayGroups splits items (newest first) by local day: "Today", "Yesterday", then dates ("Oct 12"). */
export function dayGroups(items: ActivityItem[], now: Date): { label: string; items: ActivityItem[] }[] {
  const yesterday = new Date(now.getFullYear(), now.getMonth(), now.getDate() - 1)
  const groups: { label: string; items: ActivityItem[] }[] = []
  for (const item of items) {
    const day = new Date(item.at)
    const label = sameDay(day, now) ? 'Today' : sameDay(day, yesterday) ? 'Yesterday' : formatDate(day, now)
    const last = groups.at(-1)
    if (last?.label === label) last.items.push(item)
    else groups.push({ label, items: [item] })
  }
  return groups
}

/** SHOWN_PROPERTIES is how many of an event's properties its row names. */
const SHOWN_PROPERTIES = 4

/** propertiesText names a custom event's properties in one line: "plan: pro · total: 49.95". */
export function propertiesText(properties: Record<string, unknown> | undefined): string {
  if (!properties) return ''
  const entries = Object.entries(properties)
  const shown = entries
    .slice(0, SHOWN_PROPERTIES)
    .map(([key, value]) => `${key}: ${typeof value === 'object' && value !== null ? JSON.stringify(value) : String(value)}`)
  if (entries.length > SHOWN_PROPERTIES) shown.push(`${entries.length - SHOWN_PROPERTIES} more`)
  return shown.join(' · ')
}

/**
 * loadActivity loads the newest `count` items of a user's activity, page
 * after page through next, and joins them: what a poll refreshes is
 * everything "Show older" has opened so far. get is the API call (tests
 * pass a fake).
 */
export async function loadActivity(
  projectId: number,
  userId: string,
  count: number,
  signal: AbortSignal,
  pageSize = ACTIVITY_PAGE_SIZE,
  get: typeof getUserActivity = getUserActivity,
): Promise<UserActivity> {
  let page = await get(projectId, userId, { limit: Math.min(pageSize, count) }, signal)
  const items = [...page.items]
  while (page.next && items.length < count) {
    page = await get(projectId, userId, { before: page.next, limit: Math.min(pageSize, count - items.length) }, signal)
    if (page.items.length === 0) break
    items.push(...page.items)
  }
  return { ...page, items }
}
