import { type FormEvent, useEffect, useState } from 'react'
import { useNavigate } from 'react-router'
import { ApiError, getAuthStatus, setup } from '../api/client'
import { TOOL_NAME } from '../brand'
import { AuthLayout } from '../components/AuthLayout'
import button from '../components/Button.module.css'
import form from '../components/Form.module.css'
import { problem } from '../lib/signin'

// SetupPage creates the first account, an admin. Once it exists the page
// sends everyone to the login page: people join by invite.
export function SetupPage() {
  const navigate = useNavigate()
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  useEffect(() => {
    const controller = new AbortController()
    getAuthStatus(controller.signal).then(
      (status) => {
        if (!status.setupNeeded) navigate('/login', { replace: true })
      },
      () => {},
    )
    return () => controller.abort()
  }, [navigate])

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setBusy(true)
    setError('')
    try {
      await setup(email, password)
      navigate('/', { replace: true })
    } catch (err) {
      if (err instanceof ApiError && err.status === 404) {
        navigate('/login', { replace: true }) // someone else was faster
        return
      }
      setError(problem(err))
      setBusy(false)
    }
  }

  return (
    <AuthLayout
      title="Create the first account"
      lead={`You become the admin of ${TOOL_NAME}. After this, people join with an invite from you.`}
    >
      <form className={form.stack} onSubmit={submit}>
        <label className={form.field}>
          <span className={form.label}>Email</span>
          <input className={form.input} type="email" autoComplete="username" required value={email} onChange={(e) => setEmail(e.target.value)} />
        </label>
        <label className={form.field}>
          <span className={form.label}>Password</span>
          <input
            className={form.input}
            type="password"
            autoComplete="new-password"
            required
            minLength={10}
            value={password}
            onChange={(e) => setPassword(e.target.value)}
          />
          <span className={form.hint}>At least 10 characters.</span>
        </label>
        {error && (
          <p className={form.error} role="alert">
            {error}
          </p>
        )}
        <div className={form.buttons}>
          <button type="submit" className={`${button.button} ${button.large} ${button.primary}`} disabled={busy}>
            Create account
          </button>
        </div>
      </form>
    </AuthLayout>
  )
}
