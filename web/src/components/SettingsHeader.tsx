import type { ReactNode } from 'react'
import { NavLink } from 'react-router'
import { useMe } from '../layout/MeContext'
import page from '../pages/Page.module.css'
import styles from './SettingsHeader.module.css'

/**
 * SettingsHeader is the top of every Settings page: "Settings" above the
 * headline (with links to the other Settings pages for admins), the
 * sentence under it, and the page's buttons on the right.
 */
export function SettingsHeader({ title, lead, actions }: { title: string; lead: ReactNode; actions?: ReactNode }) {
  const me = useMe()
  return (
    <header className={styles.header}>
      <div className={styles.heading}>
        <nav className={`${page.breadcrumb} ${styles.nav}`} aria-label="Settings pages">
          <span className={styles.label}>Settings</span>
          {me?.role === 'admin' && (
            <>
              <NavLink to="/settings/projects" end={false}>
                Projects
              </NavLink>
              <NavLink to="/settings/users">Users</NavLink>
              <NavLink to="/settings/api-tokens">API tokens</NavLink>
              <NavLink to="/settings/status">Status</NavLink>
              <NavLink to="/settings/account">Account</NavLink>
            </>
          )}
        </nav>
        <h1 className={page.title}>{title}</h1>
        <p className={page.lead}>{lead}</p>
      </div>
      {actions && <div className={styles.actions}>{actions}</div>}
    </header>
  )
}
