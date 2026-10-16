import { NavLink, Outlet } from 'react-router'
import { TOOL_NAME } from '../brand'
import styles from './AppShell.module.css'

// AppShell is the frame around every page: the sidebar on the left and the
// current page on the right.
export function AppShell() {
  return (
    <div className={styles.shell}>
      <aside className={styles.sidebar}>
        <div className={styles.brand}>
          <Logo />
          <span>{TOOL_NAME}</span>
        </div>
        <nav className={styles.nav}>
          <NavLink
            to="/"
            end
            className={({ isActive }) => (isActive ? `${styles.navItem} ${styles.active}` : styles.navItem)}
          >
            All projects
          </NavLink>
        </nav>
      </aside>
      <main className={styles.main}>
        <Outlet />
      </main>
    </div>
  )
}

// Logo is a three by three grid of squares with one orange corner.
function Logo() {
  const squares = []
  for (let row = 0; row < 3; row++) {
    for (let col = 0; col < 3; col++) {
      const accent = row === 2 && col === 2
      squares.push(
        <rect
          key={`${row}-${col}`}
          x={col * 7}
          y={row * 7}
          width={6}
          height={6}
          rx={1}
          className={accent ? styles.logoAccent : styles.logoSquare}
        />,
      )
    }
  }
  return (
    <svg className={styles.logo} viewBox="0 0 20 20" aria-hidden="true">
      {squares}
    </svg>
  )
}
