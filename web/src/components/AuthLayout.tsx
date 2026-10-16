import type { ReactNode } from 'react'
import { TOOL_NAME } from '../brand'
import page from '../pages/Page.module.css'
import styles from './AuthLayout.module.css'
import { Logo } from './Logo'

/**
 * AuthLayout frames the pages that sign people in (setup, login, invite,
 * forgot and reset password): the brand, a headline with a sentence under
 * it, and the form in a card, in the middle of the page.
 */
export function AuthLayout({ title, lead, children, footer }: { title: string; lead?: ReactNode; children?: ReactNode; footer?: ReactNode }) {
  return (
    <main className={styles.page}>
      <div className={styles.column}>
        <div className={styles.brand}>
          <Logo />
          <span>{TOOL_NAME}</span>
        </div>
        <h1 className={page.title}>{title}</h1>
        {lead && <p className={page.lead}>{lead}</p>}
        {children && <div className={styles.card}>{children}</div>}
        {footer && <p className={styles.footer}>{footer}</p>}
      </div>
    </main>
  )
}
