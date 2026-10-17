import { useEffect, useState } from 'react'
import { copyText } from '../lib/clipboard'
import button from './Button.module.css'
import styles from './CopyBlock.module.css'
import { CheckIcon, CopyIcon } from './Icons'

/** COPIED_MS is how long the button says "Copied". */
const COPIED_MS = 2000

/**
 * CopyBlock shows a value or a piece of code in mono, with a button that
 * copies it: a DSN, a command, a snippet, a link to hand over.
 */
export function CopyBlock({ value, label }: { value: string; label: string }) {
  const [copied, setCopied] = useState(false)
  useEffect(() => {
    if (!copied) return
    const timer = setTimeout(() => setCopied(false), COPIED_MS)
    return () => clearTimeout(timer)
  }, [copied])

  return (
    <div className={styles.block}>
      <pre className={styles.code} aria-label={label}>
        <code>{value}</code>
      </pre>
      <button
        type="button"
        className={`${button.button} ${styles.copy}`}
        onClick={async () => setCopied(await copyText(value))}
        aria-label={copied ? `${label} copied` : `Copy ${label.toLowerCase()}`}
      >
        {copied ? <CheckIcon /> : <CopyIcon />}
        {copied ? 'Copied' : 'Copy'}
      </button>
    </div>
  )
}
