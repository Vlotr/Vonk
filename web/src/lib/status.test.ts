import { expect, test } from 'vitest'
import type { ToolStatus } from '../api/client'
import { adminAlertLabel, diskFree, formatBytes, formatUptime, statusHeadline } from './status'

function status(over: Partial<ToolStatus> = {}): ToolStatus {
  return {
    version: '1.2.0',
    startedAt: '2026-10-16T10:00:00Z',
    uptimeSeconds: 3600,
    healthy: true,
    failing: [],
    db: 'ok',
    disk: 'ok',
    databaseBytes: 1_500_000,
    diskFreeBytes: 40 * 2 ** 30,
    diskTotalBytes: 100 * 2 ** 30,
    eventsLastHour: 12,
    queueDepth: 0,
    queueCapacity: 10_000,
    lastBackup: null,
    adminAlerts: [],
    adminSlackWebhook: null,
    mailEnabled: true,
    admins: 2,
    ...over,
  }
}

test('sizes read the way people read them', () => {
  expect(formatBytes(512)).toBe('512 B')
  expect(formatBytes(1536)).toBe('1.5 KiB')
  expect(formatBytes(1_500_000)).toBe('1.4 MiB')
  expect(formatBytes(40 * 2 ** 30)).toBe('40 GiB')
  expect(formatBytes(3.25 * 2 ** 40)).toBe('3.3 TiB')
})

test('uptime in the largest units that fit', () => {
  expect(formatUptime(30)).toBe('less than a minute')
  expect(formatUptime(12 * 60)).toBe('12 minutes')
  expect(formatUptime(3600 + 5 * 60)).toBe('1 hour 5 minutes')
  expect(formatUptime(3 * 86400 + 7200)).toBe('3 days 2 hours')
  expect(formatUptime(86400)).toBe('1 day')
})

test('the headline names what is wrong', () => {
  expect(statusHeadline(status())).toBe('Everything works')
  expect(statusHeadline(status({ healthy: false, failing: ['disk'], disk: 'low' }))).toBe('The disk is almost full')
  expect(statusHeadline(status({ healthy: false, failing: ['disk'], disk: 'error' }))).toBe('The free disk space cannot be read')
  expect(statusHeadline(status({ healthy: false, failing: ['db'], db: 'error' }))).toBe('The database does not answer')
  expect(statusHeadline(status({ healthy: false, failing: ['db', 'disk'], db: 'error', disk: 'low' }))).toBe('2 problems need attention')
})

test('disk space as free, then of total with the percentage', () => {
  expect(diskFree(status())).toEqual({ value: '40 GiB', detail: 'of 100 GiB (40 %)' })
  expect(diskFree(status({ diskFreeBytes: 2 ** 29, diskTotalBytes: 8 * 2 ** 30 }))).toEqual({ value: '512 MiB', detail: 'of 8 GiB (6.3 %)' })
  expect(diskFree(status({ disk: 'error', diskFreeBytes: 0, diskTotalBytes: 0 }))).toEqual({ value: 'Cannot be read', detail: '' })
})

test('admin alert kinds have names', () => {
  expect(adminAlertLabel('disk_low')).toBe('Disk space low')
  expect(adminAlertLabel('db_write_failed')).toBe('Events not stored')
  expect(adminAlertLabel('queue_drops')).toBe('Events turned away')
  expect(adminAlertLabel('something_new')).toBe('something_new')
})
