import { Link } from 'react-router'
import page from './Page.module.css'

// NotFoundPage is shown for a URL the dashboard does not know, and for a
// project or issue that does not exist (or that the viewer may not see).
export function NotFoundPage({ what = 'Page' }: { what?: 'Page' | 'Project' | 'Issue' }) {
  return (
    <section>
      <h1 className={page.title}>{`${what} not found`}</h1>
      <p className={page.lead}>
        <Link to="/">Go to All projects</Link>
      </p>
    </section>
  )
}
