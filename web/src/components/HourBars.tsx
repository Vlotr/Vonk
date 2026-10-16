import { plural } from '../lib/format'
import { barLevels } from '../lib/levels'
import styles from './HourBars.module.css'

interface HourBarsProps {
  /** Events per hour, oldest first; the last entry is the current hour. */
  counts: number[]
  /** bars are the thin ones in the issue list, blocks the large ones on the issue page. */
  variant: 'bars' | 'blocks'
}

// HourBars draws events per hour in five shades, relative to the busiest hour.
export function HourBars({ counts, variant }: HourBarsProps) {
  const levels = barLevels(counts)
  const total = counts.reduce((sum, n) => sum + n, 0)
  return (
    <div
      className={variant === 'bars' ? styles.bars : styles.blocks}
      role="img"
      aria-label={`${plural(total, 'event')} in the last 24 hours`}
    >
      {levels.map((level, i) => {
        const hoursAgo = counts.length - 1 - i
        return (
          <span
            key={i}
            className={`${styles.hour} ${styles[`level${level}`]}`}
            title={`${plural(counts[i], 'event')}, ${hoursAgo === 0 ? 'this hour' : `${hoursAgo} h ago`}`}
          />
        )
      })}
    </div>
  )
}
