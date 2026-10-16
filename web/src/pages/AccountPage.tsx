import { type FormEvent, useState } from 'react'
import { changePassword, logout } from '../api/client'
import button from '../components/Button.module.css'
import form from '../components/Form.module.css'
import { SettingsHeader } from '../components/SettingsHeader'
import { useMe } from '../layout/MeContext'
import { roleLabel } from '../lib/settings'
import { problem } from '../lib/signin'
import page from './Page.module.css'
import styles from './AccountPage.module.css'

// AccountPage is for everyone: who you are signed in as, a new password,
// and signing out.
export function AccountPage() {
  const me = useMe()
  const [current, setCurrent] = useState('')
  const [next, setNext] = useState('')
  const [error, setError] = useState('')
  const [changed, setChanged] = useState(false)
  const [busy, setBusy] = useState(false)

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setBusy(true)
    setError('')
    setChanged(false)
    try {
      await changePassword(current, next)
      setChanged(true)
      setCurrent('')
      setNext('')
    } catch (err) {
      setError(problem(err))
    } finally {
      setBusy(false)
    }
  }

  const signOut = async () => {
    try {
      await logout()
    } finally {
      window.location.assign('/login')
    }
  }

  if (!me) return <section aria-busy="true" />
  return (
    <section>
      <SettingsHeader
        title="Your account"
        lead={`You are signed in as ${me.email}, ${roleLabel(me.role).toLowerCase()}.`}
        actions={
          <button type="button" className={`${button.button} ${button.large}`} onClick={signOut}>
            Sign out
          </button>
        }
      />
      <section className={`${page.card} ${styles.card}`} aria-labelledby="password-title">
        <h2 id="password-title" className={form.title}>
          Change password
        </h2>
        <form className={styles.form} onSubmit={submit}>
          <input type="email" autoComplete="username" value={me.email} readOnly hidden />
          <label className={form.field}>
            <span className={form.label}>Current password</span>
            <input
              className={form.input}
              type="password"
              autoComplete="current-password"
              required
              value={current}
              onChange={(e) => setCurrent(e.target.value)}
            />
          </label>
          <label className={form.field}>
            <span className={form.label}>New password</span>
            <input
              className={form.input}
              type="password"
              autoComplete="new-password"
              required
              minLength={10}
              value={next}
              onChange={(e) => setNext(e.target.value)}
            />
            <span className={form.hint}>At least 10 characters. Changing it signs you out everywhere else.</span>
          </label>
          {error && (
            <p className={form.error} role="alert">
              {error}
            </p>
          )}
          {changed && (
            <p className={form.success} role="status">
              Your password is changed. Other browsers and devices are signed out.
            </p>
          )}
          <div className={form.buttons}>
            <button type="submit" className={`${button.button} ${button.large} ${button.primary}`} disabled={busy}>
              Change password
            </button>
          </div>
        </form>
      </section>
    </section>
  )
}
