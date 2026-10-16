import { useEffect, useState } from 'react'
import { previewRoutes, type RoutePreview } from '../api/client'
import form from '../components/Form.module.css'
import { parsePatterns } from '../lib/settings'
import { problem } from '../lib/signin'
import styles from './RoutePatternsField.module.css'

/** PREVIEW_DELAY_MS waits for a pause in typing before the preview asks the server. */
const PREVIEW_DELAY_MS = 300

interface RoutePatternsFieldProps {
  projectId: number
  /** text is the field's content: one pattern per line. */
  text: string
  onChange: (text: string) => void
}

// RoutePatternsField is the route patterns of Edit project, one per line,
// with a preview of the project's recent paths and the route each gets
// under the patterns typed (the server's own rules, nothing saved). Paths
// show with their secrets masked, as they are stored.
export function RoutePatternsField({ projectId, text, onChange }: RoutePatternsFieldProps) {
  const [preview, setPreview] = useState<RoutePreview[]>()
  const [error, setError] = useState('')
  const patterns = parsePatterns(text).join('\n') // what the preview depends on

  useEffect(() => {
    let current = true
    const timer = setTimeout(() => {
      previewRoutes(projectId, patterns === '' ? [] : patterns.split('\n')).then(
        (answer) => {
          if (!current) return
          setPreview(answer.paths)
          setError('')
        },
        (err: unknown) => {
          if (current) setError(problem(err))
        },
      )
    }, PREVIEW_DELAY_MS)
    return () => {
      current = false
      clearTimeout(timer)
    }
  }, [projectId, patterns])

  return (
    <div className={styles.patterns}>
      <label className={form.field}>
        <span className={form.label}>Route patterns</span>
        <span className={form.hint}>
          One per line, such as <code>/products/:slug</code>: a part that starts with a colon stands for any one segment. Without a pattern,
          numbers, ids and tokens in a path become <code>:id</code>, <code>:uuid</code>, <code>:hash</code> or <code>:token</code>.
        </span>
        <textarea
          className={`${form.input} ${styles.textarea}`}
          rows={4}
          spellCheck={false}
          placeholder="/products/:slug"
          value={text}
          onChange={(e) => onChange(e.target.value)}
        />
      </label>
      <p className={form.note}>Patterns apply to page views and errors that come in from now on. Routes already stored stay as they are.</p>

      <div className={styles.preview} aria-live="polite">
        <span className={form.label}>Recent paths</span>
        {error ? (
          <p className={form.error}>{error}</p>
        ) : preview === undefined ? null : preview.length === 0 ? (
          <p className={form.note}>No page views or errors yet to try the patterns on.</p>
        ) : (
          <table className={styles.table}>
            <thead>
              <tr>
                <th scope="col">Path</th>
                <th scope="col">Route</th>
              </tr>
            </thead>
            <tbody>
              {preview.map((p) => (
                <tr key={p.path}>
                  <td>{p.path}</td>
                  <td>{p.route}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
    </div>
  )
}
