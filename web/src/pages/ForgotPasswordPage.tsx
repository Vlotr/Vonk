import { type FormEvent, useEffect, useState } from 'react'
import { Link } from 'react-router'
import { type AuthStatus, forgotPassword, getAuthStatus } from '../api/client'
import { AuthLayout } from '../components/AuthLayout'
import button from '../components/Button.module.css'
import { CopyBlock } from '../components/CopyBlock'
import form from '../components/Form.module.css'
import { problem } from '../lib/signin'

// ForgotPasswordPage mails a reset link when the server can send mail. It
// says the same whether or not the address has an account. Without mail,
// it says who can help: an admin for a member, the server for an admin.
export function ForgotPasswordPage() {
  const [status, setStatus] = useState<AuthStatus>()
  const [email, setEmail] = useState('')
  const [sentTo, setSentTo] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  useEffect(() => {
    const controller = new AbortController()
    getAuthStatus(controller.signal).then(setStatus, (err: unknown) => setError(problem(err)))
    return () => controller.abort()
  }, [])

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setBusy(true)
    setError('')
    try {
      await forgotPassword(email)
      setSentTo(email.trim())
    } catch (err) {
      setError(problem(err))
    } finally {
      setBusy(false)
    }
  }

  const back = <Link to="/login">Back to sign in</Link>
  if (!status) return <AuthLayout title="Reset your password" lead={error || undefined} footer={back} />

  if (!status.mailEnabled) {
    return (
      <AuthLayout
        title="Reset your password"
        lead="This server does not send mail. A member asks an admin, who makes a reset link in Settings, under Users."
        footer={back}
      >
        <div className={form.stack}>
          <p className={form.note}>An admin resets their own password on the server, with this command:</p>
          <CopyBlock label="Command" value="monitor admin reset-password --email you@example.com" />
        </div>
      </AuthLayout>
    )
  }

  if (sentTo) {
    return (
      <AuthLayout
        title="Check your mail"
        lead={`If ${sentTo} has an account, a link to reset its password is on its way. It works once, for one hour.`}
        footer={back}
      />
    )
  }

  return (
    <AuthLayout title="Reset your password" lead="We mail you a link to choose a new password." footer={back}>
      <form className={form.stack} onSubmit={submit}>
        <label className={form.field}>
          <span className={form.label}>Email</span>
          <input className={form.input} type="email" autoComplete="username" required value={email} onChange={(e) => setEmail(e.target.value)} />
        </label>
        {error && (
          <p className={form.error} role="alert">
            {error}
          </p>
        )}
        <div className={form.buttons}>
          <button type="submit" className={`${button.button} ${button.large} ${button.primary}`} disabled={busy}>
            Send link
          </button>
        </div>
      </form>
    </AuthLayout>
  )
}
