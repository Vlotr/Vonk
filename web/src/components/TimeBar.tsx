import { formatPageTime } from '../lib/pages'
import styles from './TimeBar.module.css'

// TimeBar is the average time on a page in the Pages table: a bar as long
// as the time, relative to the longest of the table, and the time itself.
// Without a time (nobody left the page yet) the bar stays empty.
export function TimeBar({ ms, maxMs }: { ms: number | null; maxMs: number }) {
  const share = ms === null || maxMs <= 0 ? 0 : Math.min(1, ms / maxMs)
  return (
    <div className={styles.time}>
      <span className={styles.track} aria-hidden="true">
        <span className={styles.fill} style={{ width: `${share * 100}%` }} />
      </span>
      <span className={styles.value}>
        {ms === null ? <span className={styles.none}>No time yet</span> : formatPageTime(ms)}
      </span>
    </div>
  )
}
