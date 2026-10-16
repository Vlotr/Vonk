import type { ReactNode } from 'react'
import styles from './Tag.module.css'

interface TagProps {
  children: ReactNode
  /** "new" is the orange New badge; "neutral" a grey tag such as the source. */
  tone?: 'neutral' | 'new'
  /** outline draws a white tag with a border, as on the issue page. */
  outline?: boolean
}

export function Tag({ children, tone = 'neutral', outline = false }: TagProps) {
  const classes = [styles.tag, tone === 'new' ? styles.new : styles.neutral, outline && tone === 'neutral' ? styles.outline : '']
  return <span className={classes.filter(Boolean).join(' ')}>{children}</span>
}
