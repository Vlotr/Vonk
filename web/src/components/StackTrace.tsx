import { useState } from 'react'
import type { EventDetail, Frame } from '../api/client'
import { displayFile, hiddenFramesSentence, shortFunction } from '../lib/format'
import { codeLines, visibleFrames } from '../lib/frames'
import styles from './StackTrace.module.css'

// StackTrace shows the newest event's exception and its frames, the most
// recent call first. Framework frames (not in-app) are hidden until asked
// for; the first in-app frame with source opens with the error line marked.
export function StackTrace({ event }: { event: EventDetail }) {
  const [showFramework, setShowFramework] = useState(false)
  const firstWithCode = event.frames.findIndex((f) => f.inApp && codeLines(f).length > 0)
  const [open, setOpen] = useState<Set<number>>(() => new Set(firstWithCode >= 0 ? [firstWithCode] : []))

  const { shown, hidden } = visibleFrames(event.frames, showFramework)
  const hasFramework = event.frames.some((f) => !f.inApp) && event.frames.some((f) => f.inApp)

  const toggle = (index: number) =>
    setOpen((prev) => {
      const next = new Set(prev)
      if (next.has(index)) next.delete(index)
      else next.add(index)
      return next
    })

  return (
    <section className={styles.card} aria-labelledby="stack-trace-title">
      <header className={styles.header}>
        <h2 id="stack-trace-title" className={styles.title}>
          Stack trace
        </h2>
        {hasFramework && (
          <button type="button" className={styles.button} onClick={() => setShowFramework((v) => !v)}>
            {showFramework ? 'Hide framework code' : 'Show framework code'}
          </button>
        )}
      </header>

      {event.exception && (
        <pre className={styles.exception}>
          {event.exception.type}
          {event.exception.value ? `: ${event.exception.value}` : ''}
        </pre>
      )}

      {event.frames.length === 0 ? (
        <p className={styles.empty}>This event has no stack trace.</p>
      ) : (
        <ol className={styles.frames}>
          {shown.map((frame) => {
            const index = event.frames.indexOf(frame)
            return <FrameItem key={index} frame={frame} open={open.has(index)} onToggle={() => toggle(index)} />
          })}
        </ol>
      )}

      {hidden > 0 && (
        <button type="button" className={styles.hiddenBar} onClick={() => setShowFramework(true)}>
          {hiddenFramesSentence(hidden)}
        </button>
      )}
    </section>
  )
}

function FrameItem({ frame, open, onToggle }: { frame: Frame; open: boolean; onToggle: () => void }) {
  const lines = codeLines(frame)
  const where = [frame.function && `in ${shortFunction(frame.function)}`, frame.line > 0 && `line ${frame.line}`]
    .filter(Boolean)
    .join(', ')
  const heading = (
    <>
      <code className={styles.file} title={frame.file}>
        {displayFile(frame.file)}
      </code>
      {where && (
        <span className={styles.where} title={frame.function || undefined}>
          {where}
        </span>
      )}
    </>
  )

  return (
    <li className={frame.inApp ? styles.frame : `${styles.frame} ${styles.framework}`}>
      {lines.length > 0 ? (
        <button type="button" className={styles.frameHeading} aria-expanded={open} onClick={onToggle}>
          {heading}
        </button>
      ) : (
        <div className={styles.frameHeading}>{heading}</div>
      )}
      {open && lines.length > 0 && <Code lines={lines} />}
    </li>
  )
}

function Code({ lines }: { lines: ReturnType<typeof codeLines> }) {
  const digits = String(lines[lines.length - 1].number).length
  return (
    <div className={styles.code}>
      <div className={styles.codeLines}>
        {lines.map((line) => (
          <div
            key={line.number}
            className={line.isError ? `${styles.line} ${styles.errorLine}` : styles.line}
            aria-current={line.isError ? 'true' : undefined}
          >
            <span className={styles.lineNumber} style={{ width: `${digits}ch` }}>
              {line.number}
            </span>
            <code className={styles.lineText}>{line.text}</code>
          </div>
        ))}
      </div>
    </div>
  )
}
