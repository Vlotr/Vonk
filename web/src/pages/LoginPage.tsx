import { type FormEvent, useEffect, useState } from 'react'
import { Link, useNavigate, useSearchParams } from 'react-router'
import { getAuthStatus, login } from '../api/client'
import { TOOL_NAME } from '../brand'
import { AuthLayout } from '../components/AuthLayout'
import button from '../components/Button.module.css'
import form from '../components/Form.module.css'
import { problem, safeNext } from '../lib/signin'

// LoginPage signs someone in and sends them on to where they wanted to go
// (?next=, a page of this site only).
export function LoginPage() {
  const navigate = useNavigate()
  const [params] = useSearchParams()
  const next = safeNext(params.get('next'))
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  // Before the first account exists, the setup page comes first.
  useEffect(() => {
    const controller = new AbortController()
    getAuthStatus(controller.signal).then(
      (status) => {
        if (status.setupNeeded) navigate('/setup', { replace: true })
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
      await login(email, password)
      navigate(next, { replace: true })
    } catch (err) {
      setError(problem(err))
      setBusy(false)
    }
  }

  return (
    <AuthLayout title={`Sign in to ${TOOL_NAME}`} footer={<Link to="/forgot-password">Forgot your password?</Link>}>
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
            autoComplete="current-password"
            required
            value={password}
            onChange={(e) => setPassword(e.target.value)}
          />
        </label>
        {error && (
          <p className={form.error} role="alert">
            {error}
          </p>
        )}
        <div className={form.buttons}>
          <button type="submit" className={`${button.button} ${button.large} ${button.primary}`} disabled={busy}>
            Sign in
          </button>
        </div>
      </form>
    </AuthLayout>
  )
}
