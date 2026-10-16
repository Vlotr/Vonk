import type { PageRange } from '../api/client'
import { RANGES } from '../lib/pages'
import styles from './PeriodTabs.module.css'

/** parseRange reads ?range=; 7 days, as the Pages design shows, when it is missing or unknown. */
export function parseRange(value: string | null): PageRange {
  return value === '24h' || value === '30d' ? value : '7d'
}

// PeriodTabs is the period toggle of the Pages and Events screens (24
// hours, 7 days, 30 days), drawn like the status tabs of the issue list.
export function PeriodTabs({ range, onChange }: { range: PageRange; onChange: (range: PageRange) => void }) {
  return (
    <div className={styles.tabs} role="group" aria-label="Period">
      {RANGES.map((r) => (
        <button key={r.range} type="button" className={styles.tab} aria-pressed={r.range === range} onClick={() => onChange(r.range)}>
          {r.label}
        </button>
      ))}
    </div>
  )
}
