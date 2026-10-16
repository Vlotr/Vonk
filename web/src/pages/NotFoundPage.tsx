import { Link } from 'react-router'
import styles from './Page.module.css'

// NotFoundPage is shown for any URL the dashboard does not know.
export function NotFoundPage() {
  return (
    <section>
      <h1 className={styles.title}>Page not found</h1>
      <p className={styles.lead}>
        <Link to="/">Go to All projects</Link>
      </p>
    </section>
  )
}
