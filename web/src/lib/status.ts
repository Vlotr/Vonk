// Text for Settings → Status. Pure functions, tested in status.test.ts.

import type { ToolStatus } from '../api/client'
import { plural } from './format'

/** oneDecimal writes 1.5 as "1.5" and 40.0 as "40". */
function oneDecimal(n: number): string {
  return String(Math.round(n * 10) / 10)
}

/** formatBytes writes a size the way people read it: "512 B", "1.4 MiB", "40 GiB". */
export function formatBytes(bytes: number): string {
  const units = ['KiB', 'MiB', 'GiB', 'TiB', 'PiB']
  if (bytes < 1024) return `${bytes} B`
  let value = bytes / 1024
  let unit = 0
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024
    unit++
  }
  return `${oneDecimal(value)} ${units[unit]}`
}

/** formatUptime writes how long the server has run: "12 minutes", "1 hour 5 minutes", "3 days 2 hours". */
export function formatUptime(seconds: number): string {
  const minutes = Math.floor(seconds / 60)
  const hours = Math.floor(minutes / 60)
  const days = Math.floor(hours / 24)
  const join = (big: string, small: number, word: string) => (small > 0 ? `${big} ${plural(small, word)}` : big)
  if (days > 0) return join(plural(days, 'day'), hours % 24, 'hour')
  if (hours > 0) return join(plural(hours, 'hour'), minutes % 60, 'minute')
  if (minutes > 0) return plural(minutes, 'minute')
  return 'less than a minute'
}

/** statusHeadline is the conclusion of the status page. */
export function statusHeadline(s: ToolStatus): string {
  if (s.failing.length > 1) return `${s.failing.length} problems need attention`
  if (s.db === 'error') return 'The database does not answer'
  if (s.disk === 'low') return 'The disk is almost full'
  if (s.disk === 'error') return 'The free disk space cannot be read'
  return 'Everything works'
}

/** diskFree is the disk's free space and the line under it: "40 GiB", "of 100 GiB (40 %)". */
export function diskFree(s: ToolStatus): { value: string; detail: string } {
  if (s.disk === 'error' || s.diskTotalBytes === 0) return { value: 'Cannot be read', detail: '' }
  const percent = (s.diskFreeBytes / s.diskTotalBytes) * 100
  return { value: formatBytes(s.diskFreeBytes), detail: `of ${formatBytes(s.diskTotalBytes)} (${oneDecimal(percent)} %)` }
}

const ADMIN_ALERTS: Record<string, string> = {
  disk_low: 'Disk space low',
  db_write_failed: 'Events not stored',
  queue_drops: 'Events turned away',
}

/** adminAlertLabel names a kind of admin alert. */
export function adminAlertLabel(kind: string): string {
  return ADMIN_ALERTS[kind] ?? kind
}
