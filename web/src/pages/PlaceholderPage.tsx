import { TOOL_NAME } from '../brand'
import styles from './Page.module.css'

// PlaceholderPage holds the home route until the real screens exist.
export function PlaceholderPage() {
  return (
    <section>
      <h1 className={styles.title}>{TOOL_NAME} is running</h1>
      <p className={styles.lead}>Errors and page views will show up here once the dashboard screens are built.</p>
    </section>
  )
}
