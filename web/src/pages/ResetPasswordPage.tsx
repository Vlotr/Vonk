import { type FormEvent, useState } from 'react'
import { Link, useLocation, useNavigate } from 'react-router'
import { ApiError, checkReset, resetPassword } from '../api/client'
import { AuthLayout } from '../components/AuthLayout'
import button from '../components/Button.module.css'
import form from '../components/Form.module.css'
import { problem, tokenFromHash } from '../lib/signin'
import { useLinkCheck } from '../lib/useLinkCheck'

// ResetPasswordPage is where a reset link leads (/reset-password#token=…),
// from a mail, an admin or the server command. The new password signs the
// account out everywhere else and in here.
export function ResetPasswordPage() {
  const navigate = useNavigate()
  const token = tokenFromHash(useLocation().hash)
  const reset = useLinkCheck(token, checkReset)
  const [password, setPassword] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setBusy(true)
    setError('')
    try {
      await resetPassword(token, password)
      navigate('/', { replace: true })
    } catch (err) {
      setError(err instanceof ApiError && err.status === 410 ? 'This link has just expired or was used. Ask for a new one.' : problem(err))
      setBusy(false)
    }
  }

  const again = <Link to="/forgot-password">Ask for a new link</Link>
  switch (reset.state) {
    case 'checking':
      return <AuthLayout title="Choose a new password" />
    case 'gone':
      return <AuthLayout title="This link no longer works" lead="It has expired or was already used. A reset link works once, for one hour." footer={again} />
    case 'invalid':
      return <AuthLayout title="This reset link is not valid" lead="Check that you opened the whole link." footer={again} />
    case 'failed':
      return <AuthLayout title="Choose a new password" lead={problem(reset.error)} />
  }

  return (
    <AuthLayout title="Choose a new password" lead={`For ${reset.data.email}. This signs you out everywhere else.`}>
      <form className={form.stack} onSubmit={submit}>
        <input type="email" autoComplete="username" value={reset.data.email} readOnly hidden />
        <label className={form.field}>
          <span className={form.label}>New password</span>
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
            Save password
          </button>
        </div>
      </form>
    </AuthLayout>
  )
}
