import { type FormEvent, useState } from 'react'
import { Link, useLocation, useNavigate } from 'react-router'
import { acceptInvite, ApiError, checkInvite } from '../api/client'
import { TOOL_NAME } from '../brand'
import { AuthLayout } from '../components/AuthLayout'
import button from '../components/Button.module.css'
import form from '../components/Form.module.css'
import { roleLabel } from '../lib/settings'
import { problem, tokenFromHash } from '../lib/signin'
import { useLinkCheck } from '../lib/useLinkCheck'

// InvitePage is where an invite link leads (/invite#token=…): the invited
// person chooses a password and is signed in with the role and projects the
// admin gave them.
export function InvitePage() {
  const navigate = useNavigate()
  const token = tokenFromHash(useLocation().hash)
  const invite = useLinkCheck(token, checkInvite)
  const [password, setPassword] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setBusy(true)
    setError('')
    try {
      await acceptInvite(token, password)
      navigate('/', { replace: true })
    } catch (err) {
      setError(err instanceof ApiError && err.status === 410 ? 'This invite has just expired or was used. Ask an admin for a new one.' : problem(err))
      setBusy(false)
    }
  }

  switch (invite.state) {
    case 'checking':
      return <AuthLayout title={`Join ${TOOL_NAME}`} />
    case 'gone':
      return (
        <AuthLayout
          title="This invite no longer works"
          lead="It has expired or was already used. Ask an admin for a new invite."
          footer={<Link to="/login">Go to sign in</Link>}
        />
      )
    case 'invalid':
      return (
        <AuthLayout
          title="This invite link is not valid"
          lead="Check that you opened the whole link, or ask an admin for a new invite."
          footer={<Link to="/login">Go to sign in</Link>}
        />
      )
    case 'failed':
      return <AuthLayout title={`Join ${TOOL_NAME}`} lead={problem(invite.error)} />
  }

  const { email, role } = invite.data
  return (
    <AuthLayout
      title={`Join ${TOOL_NAME}`}
      lead={`You are invited as ${role === 'admin' ? 'an' : 'a'} ${roleLabel(role).toLowerCase()}. Choose a password to create your account.`}
    >
      <form className={form.stack} onSubmit={submit}>
        <label className={form.field}>
          <span className={form.label}>Email</span>
          <input className={form.input} type="email" autoComplete="username" value={email} readOnly />
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
