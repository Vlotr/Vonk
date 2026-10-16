import { Link, NavLink } from 'react-router'
import type { Project } from '../api/client'
import { TOOL_NAME } from '../brand'
import { AlertCircleIcon, GridIcon } from '../components/Icons'
import styles from './Sidebar.module.css'

function navClass({ isActive }: { isActive: boolean }): string {
  return isActive ? `${styles.navItem} ${styles.active}` : styles.navItem
}

// Sidebar is the navigation on the left. Pages, Events, Alerts and Settings
// join it in the tasks that build those screens.
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
            </div>
          </nav>
        )}
      </div>
    </aside>
  )
}

// Logo is a three by three grid of squares: grey, one light orange in the
// middle and a dark orange corner, like the errors-per-hour bars.
function Logo() {
  const squares = []
  for (let row = 0; row < 3; row++) {
    for (let col = 0; col < 3; col++) {
      let className = styles.logoSquare
      if (row === 1 && col === 1) className = styles.logoMiddle
      if (row === 2 && col === 2) className = styles.logoCorner
      squares.push(<rect key={`${row}-${col}`} x={col * 7} y={row * 7} width={6} height={6} rx={1.5} className={className} />)
    }
  }
  return (
    <svg className={styles.logo} viewBox="0 0 20 20" aria-hidden="true">
      {squares}
    </svg>
  )
}
