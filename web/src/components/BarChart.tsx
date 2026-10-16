import styles from './BarChart.module.css'

export interface Bar {
  /** label goes under the bar ("Oct 16"); title is its tooltip ("Oct 16: 17,420 page views"). */
  label: string
  title: string
  value: number
}

interface BarChartProps {
  /** bars, oldest first; the last one (now) is drawn darker. */
  bars: Bar[]
  /** label names what the chart shows, for screen readers. */
  label: string
  /** values shows each bar's number above it (for a few wide bars). */
  values: boolean
  /** labelEvery labels every n-th bar, counted back from the last. */
  labelEvery: number
}

// BarChart draws values as bars, tallest at the busiest, with a line under
// them and labels below, as the Pages design does. Plain divs, no library.
export function BarChart({ bars, label, values, labelEvery }: BarChartProps) {
  const max = Math.max(0, ...bars.map((b) => b.value))
  const last = bars.length - 1
  return (
    <figure className={styles.chart} aria-label={label}>
      <div className={styles.plot}>
        {bars.map((bar, i) => (
          <div key={i} className={styles.column} title={bar.title}>
            {values && <span className={styles.value}>{bar.value.toLocaleString('en-US')}</span>}
            <span
              className={`${styles.bar} ${i === last ? styles.latest : ''}`}
              style={{ height: max === 0 ? 0 : `calc(var(--chart-bar-height) * ${bar.value / max})` }}
            />
          </div>
        ))}
      </div>
      <div className={styles.labels} aria-hidden="true">
        {bars.map((bar, i) => (
          <span key={i} className={styles.label}>
            {(last - i) % labelEvery === 0 ? bar.label : ''}
          </span>
        ))}
      </div>
      <ul className={styles.visuallyHidden}>
        {bars.map((bar, i) => (
          <li key={i}>{bar.title}</li>
        ))}
      </ul>
    </figure>
  )
}
