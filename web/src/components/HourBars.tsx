import { plural } from '../lib/format'
import { barLevels } from '../lib/levels'
import styles from './HourBars.module.css'

interface HourBarsProps {
  /** Events per hour, oldest first; the last entry is the current hour. */
  counts: number[]
  /**
   * bars are the thin ones in the issue list, wide the larger ones in the
   * All projects table, blocks the large ones on the issue page.
   */
  variant: 'bars' | 'wide' | 'blocks'
  /** max is the busiest hour of the whole table, so rows share one scale; without it the row's own busiest hour. */
  max?: number
  /** what counts: "event" (default) or "error", for the label. */
  noun?: string
}

// HourBars draws events per hour in five shades, relative to the busiest hour.
export function HourBars({ counts, variant, max, noun = 'event' }: HourBarsProps) {
  const levels = barLevels(counts, max)
  const total = counts.reduce((sum, n) => sum + n, 0)
  return (
    <div className={styles[variant]} role="img" aria-label={`${plural(total, noun)} in the last 24 hours`}>
      {levels.map((level, i) => {
        const hoursAgo = counts.length - 1 - i
        return (
          <span
            key={i}
            className={`${styles.hour} ${styles[`level${level}`]}`}
            title={`${plural(counts[i], noun)}, ${hoursAgo === 0 ? 'this hour' : `${hoursAgo} h ago`}`}
          />
        )
      })}
    </div>
  )
}

/** HourLegend explains the five shades: "none", five squares, "many". */
export function HourLegend({ label }: { label: string }) {
  return (
    <div className={styles.legend}>
      <span>{label}</span>
      <span className={styles.legendEnd}>none</span>
      <span className={styles.legendSquares} aria-hidden="true">
        {[0, 1, 2, 3, 4].map((level) => (
          <span key={level} className={`${styles.legendSquare} ${styles[`level${level}`]}`} />
        ))}
      </span>
      <span className={styles.legendEnd}>many</span>
    </div>
  )
}
