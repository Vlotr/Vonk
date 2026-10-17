import { type FormEvent, useState } from 'react'
import { deleteUserData, type Project, type UserDataDeleted } from '../api/client'
import { AdminOnly } from '../components/AdminOnly'
import button from '../components/Button.module.css'
import form from '../components/Form.module.css'
import { SettingsHeader } from '../components/SettingsHeader'
import { useProjects } from '../layout/ProjectsContext'
import { confirmDeleteSentence, deletedSentences } from '../lib/backup'
import { problem } from '../lib/signin'
import page from './Page.module.css'
import styles from './SettingsPrivacyPage.module.css'

// SettingsPrivacyPage is where admins delete everything stored about one
// person on request: the errors, page views and custom events stored with
// the user id the apps send, in every project or the ones chosen. It asks
// once more before it deletes. There is no design for this page; it
// follows Settings → API tokens.
export function SettingsPrivacyPage() {
  return (
    <AdminOnly>
      <DeleteUserData />
    </AdminOnly>
  )
}

/** Request is what the admin asked to delete, waiting for the confirmation. */
interface Request {
  userId: string
  /** projects are the chosen ones; empty means every project. */
  projects: Project[]
}

function DeleteUserData() {
  const projects = useProjects().data ?? []
  const [userId, setUserId] = useState('')
  const [everywhere, setEverywhere] = useState(true)
  const [chosen, setChosen] = useState<number[]>([])
  const [asking, setAsking] = useState<Request>()
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [done, setDone] = useState<UserDataDeleted>()

  const ask = (e: FormEvent) => {
    e.preventDefault()
    setError('')
    setDone(undefined)
    const id = userId.trim()
    if (id === '') {
      setError('Give the user id whose data should be deleted.')
      return
    }
    if (!everywhere && chosen.length === 0) {
      setError('Choose at least one project.')
      return
    }
    setAsking({ userId: id, projects: everywhere ? [] : projects.filter((p) => chosen.includes(p.id)) })
  }

  const confirm = async () => {
    if (!asking) return
    setBusy(true)
    setError('')
    try {
      const result = await deleteUserData(asking.userId, asking.projects.length > 0 ? asking.projects.map((p) => p.id) : undefined)
      setDone(result)
      setAsking(undefined)
      setUserId('')
    } catch (err) {
      setError(problem(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <section>
      <SettingsHeader
        title="Privacy"
        lead="Delete everything stored about one person, for example when they ask for it: their errors, page views and events, in every project or the ones you choose."
      />

      <section className={`${form.card} ${styles.card}`} aria-labelledby="delete-title">
        <h2 id="delete-title" className={form.title}>
          Delete the data of a user
        </h2>
        <form className={form.stack} onSubmit={ask}>
          <label className={form.field}>
            <span className={form.label}>User id</span>
            <input
              className={form.input}
              placeholder="For example: 42 or jan@example.com"
              value={userId}
              onChange={(e) => {
                setUserId(e.target.value)
                setAsking(undefined)
              }}
              disabled={busy}
            />
            <span className={form.hint}>
              The id your apps send: the user id of Sentry (Sentry.setUser) and the distinct id of PostHog (posthog.identify). It must match exactly.
            </span>
          </label>
          <fieldset className={form.fieldset} disabled={busy}>
            <legend className={`${form.label} ${form.legend}`}>Projects</legend>
            <div className={form.checks}>
              <label className={form.check}>
                <input type="radio" name="where" checked={everywhere} onChange={() => setEverywhere(true)} />
                <span>Every project</span>
              </label>
              <label className={form.check}>
                <input type="radio" name="where" checked={!everywhere} onChange={() => setEverywhere(false)} />
                <span>Only these projects</span>
              </label>
              {!everywhere && (
                <div className={`${form.checks} ${styles.projects}`}>
                  {projects.map((p) => (
                    <label key={p.id} className={form.check}>
                      <input
                        type="checkbox"
                        checked={chosen.includes(p.id)}
                        onChange={(e) => {
                          setChosen(e.target.checked ? [...chosen, p.id] : chosen.filter((id) => id !== p.id))
                          setAsking(undefined)
                        }}
                      />
                      <span>{p.name}</span>
                    </label>
                  ))}
                </div>
              )}
            </div>
          </fieldset>
          <p className={form.note}>
            Issues that only this user had are removed; the others count their events again. Backups made before keep the data until they are
            replaced, so delete downloaded backups you no longer need.
          </p>
          {error && (
            <p className={form.error} role="alert">
              {error}
            </p>
          )}
          {asking ? (
            <div className={styles.confirm} role="alertdialog" aria-labelledby="confirm-text">
              <p id="confirm-text" className={styles.ask}>
                {confirmDeleteSentence(asking.userId, asking.projects.map((p) => p.name))}
              </p>
              <div className={form.buttons}>
                <button type="button" className={`${button.button} ${button.large} ${button.danger}`} onClick={confirm} disabled={busy}>
                  Delete
                </button>
                <button type="button" className={`${button.button} ${button.large}`} onClick={() => setAsking(undefined)} disabled={busy}>
                  Cancel
                </button>
              </div>
            </div>
          ) : (
            <div className={form.buttons}>
              <button type="submit" className={`${button.button} ${button.large} ${button.primary}`}>
                Delete data
              </button>
            </div>
          )}
        </form>
      </section>

      {done && (
        <section className={`${page.card} ${styles.result}`} aria-labelledby="done-title" role="status">
          <h2 id="done-title" className={page.cardTitle}>
            Done
          </h2>
          {deletedSentences(done).map((s) => (
            <p key={s} className={styles.text}>
              {s}
            </p>
          ))}
        </section>
      )}
    </section>
  )
}
