import { Link, NavLink } from 'react-router'
import type { Project } from '../api/client'
import { TOOL_NAME } from '../brand'
import { ActivityIcon, AlertCircleIcon, BellIcon, FileIcon, GridIcon, SettingsIcon } from '../components/Icons'
import { Logo } from '../components/Logo'
import styles from './Sidebar.module.css'

function navClass({ isActive }: { isActive: boolean }): string {
  return isActive ? `${styles.navItem} ${styles.active}` : styles.navItem
}

// Sidebar is the navigation on the left, with Settings at the bottom.
// Pages and Events (both from PostHog) show for projects that track page
// views, between Issues and Alerts as the designs order them; Events also
// for a project that sends custom events without page views (from a
// server, with posthog-node).
export function Sidebar({ project }: { project?: Project }) {
  return (
    <aside className={styles.sidebar}>
      <div className={styles.inner}>
        <Link to="/" className={styles.brand}>
          <Logo />
          <span>{TOOL_NAME}</span>
        </Link>

        <nav className={styles.nav} aria-label="Main">
          <NavLink to="/" end className={navClass}>
            <GridIcon className={styles.icon} />
            All projects
          </NavLink>
        </nav>

        {project && (
          <nav className={styles.section} aria-label={project.name}>
            <div className={styles.sectionLabel}>{project.name}</div>
            <div className={styles.nav}>
              <NavLink to={`/projects/${project.id}/issues`} className={navClass}>
                <AlertCircleIcon className={styles.icon} />
                Issues
                {project.openIssues > 0 && (
                  <span className={styles.badge} aria-label={`${project.openIssues} open`}>
                    {project.openIssues}
                  </span>
                )}
              </NavLink>
              {project.tracksPageviews && (
                <NavLink to={`/projects/${project.id}/pages`} className={navClass}>
                  <FileIcon className={styles.icon} />
                  Pages
                </NavLink>
              )}
              {(project.tracksPageviews || project.hasCustomEvents) && (
                <NavLink to={`/projects/${project.id}/events`} className={navClass}>
                  <ActivityIcon className={styles.icon} />
                  Events
                </NavLink>
              )}
              <NavLink to={`/projects/${project.id}/alerts`} className={navClass}>
                <BellIcon className={styles.icon} />
                Alerts
              </NavLink>
            </div>
          </nav>
        )}

        <nav className={`${styles.nav} ${styles.bottom}`} aria-label="Settings">
          <NavLink to="/settings" className={navClass}>
            <SettingsIcon className={styles.icon} />
            Settings
          </NavLink>
        </nav>
      </div>
    </aside>
  )
}
