import type { JourneyStep } from '../api/client'
import { errorStepText } from '../lib/activity'
import { formatPageTime } from '../lib/pages'
import page from '../pages/Page.module.css'
import styles from './BeforeError.module.css'
import { ErrorMarkIcon } from './Icons'

// BeforeError is the "Before this error" card of the issue page (Issue
// detail.png): the pages the error's user saw in the half hour before it,
// numbered, oldest first, with the time on each, and the error itself as
// the last, red row. Without page views (or without a user id) there is no
// card.
export function BeforeError({ steps, errorRoute }: { steps: JourneyStep[]; errorRoute?: string }) {
  if (steps.length === 0) return null
  return (
    <section className={`${page.card} ${styles.card}`} aria-labelledby="before-error-title">
      <h2 id="before-error-title" className={page.cardTitle}>
        Before this error
      </h2>
      <p className={styles.lead}>Pages this user saw, from page tracking.</p>
      <ol className={styles.steps}>
        {steps.map((step, i) => {
          const opened = new Date(step.start)
          return (
            <li key={`${step.start}-${i}`} className={styles.step} title={`${step.route}, opened at ${opened.toLocaleTimeString()}`}>
              <span className={styles.number} aria-hidden="true">
                {i + 1}
              </span>
              <span className={styles.route}>{step.route}</span>
              {step.durationMs !== null && <span className={styles.duration}>{formatPageTime(step.durationMs)}</span>}
            </li>
          )
        })}
        <li className={`${styles.step} ${styles.error}`}>
          <span className={styles.marker} aria-hidden="true">
            <ErrorMarkIcon className={styles.mark} />
          </span>
          <span className={styles.errorText}>{errorStepText(errorRoute)}</span>
        </li>
      </ol>
    </section>
  )
}
