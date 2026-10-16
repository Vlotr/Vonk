import { type FormEvent, useState } from 'react'
import { createInvite, type Role, type SettingsProject, type SettingsUser, updateUser } from '../api/client'
import button from '../components/Button.module.css'
import form from '../components/Form.module.css'
import { ChevronDownIcon } from '../components/Icons'
import { problem } from '../lib/signin'
import styles from './UserForms.module.css'

// The forms of Settings → Users: inviting someone, and changing a user's
// role and projects.

/** RoleSelect picks Member or Admin. */
function RoleSelect({ value, onChange }: { value: Role; onChange: (role: Role) => void }) {
  return (
    <label className={form.field}>
      <span className={form.label}>Role</span>
      <span className={form.selectBox}>
        <select className={form.select} value={value} onChange={(e) => onChange(e.target.value as Role)}>
          <option value="member">Member: sees the projects below</option>
          <option value="admin">Admin: sees and manages everything</option>
        </select>
        <ChevronDownIcon className={form.chevron} />
      </span>
    </label>
  )
}

/** ProjectChecks picks a member's projects. */
function ProjectChecks({ projects, chosen, onChange }: { projects: SettingsProject[]; chosen: number[]; onChange: (ids: number[]) => void }) {
  return (
    <fieldset className={form.fieldset}>
      <legend className={`${form.label} ${form.legend}`}>Projects this member sees</legend>
      {projects.length === 0 ? (
        <p className={form.note}>There are no projects yet.</p>
      ) : (
        <div className={`${form.checks} ${styles.projectChecks}`}>
          {projects.map((p) => (
            <label key={p.id} className={form.check}>
              <input
                type="checkbox"
                checked={chosen.includes(p.id)}
                onChange={(e) => onChange(e.target.checked ? [...chosen, p.id] : chosen.filter((id) => id !== p.id))}
              />
              <span>{p.name}</span>
            </label>
          ))}
        </div>
      )}
    </fieldset>
  )
}

interface InviteFormProps {
  projects: SettingsProject[]
  mailEnabled: boolean
  onCancel: () => void
  onInvited: (email: string, link: string, mailed: boolean) => void
}

export function InviteForm({ projects, mailEnabled, onCancel, onInvited }: InviteFormProps) {
  const [email, setEmail] = useState('')
  const [role, setRole] = useState<Role>('member')
  const [chosen, setChosen] = useState<number[]>([])
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setBusy(true)
    setError('')
    try {
      const made = await createInvite(email.trim(), role, role === 'member' ? chosen : [])
      onInvited(made.invite.email, made.link, made.mailed)
    } catch (err) {
      setError(problem(err))
      setBusy(false)
    }
  }

  return (
    <section className={`${form.card} ${styles.formCard}`} aria-labelledby="invite-title">
      <h2 id="invite-title" className={form.title}>
        Invite user
      </h2>
      <form className={styles.form} onSubmit={submit}>
        <div className={form.columns}>
          <label className={form.field}>
            <span className={form.label}>Email</span>
            <input
              className={form.input}
              type="email"
              placeholder="For example: jan@example.com"
              required
              value={email}
              onChange={(e) => setEmail(e.target.value)}
              autoFocus
            />
          </label>
          <RoleSelect value={role} onChange={setRole} />
        </div>
        {role === 'member' && <ProjectChecks projects={projects} chosen={chosen} onChange={setChosen} />}
        <p className={form.note}>
          You get a link that works once, for 7 days.{' '}
          {mailEnabled ? 'We also mail it to the address above.' : 'This server does not send mail, so you send the link yourself.'}
        </p>
        {error && (
          <p className={form.error} role="alert">
            {error}
          </p>
        )}
        <div className={form.buttons}>
          <button type="submit" className={`${button.button} ${button.large} ${button.primary}`} disabled={busy}>
            Create invite
          </button>
          <button type="button" className={`${button.button} ${button.large}`} onClick={onCancel}>
            Cancel
          </button>
        </div>
      </form>
    </section>
  )
}

interface EditUserFormProps {
  user: SettingsUser
  projects: SettingsProject[]
  onCancel: () => void
  onSaved: () => void
}

export function EditUserForm({ user, projects, onCancel, onSaved }: EditUserFormProps) {
  const [role, setRole] = useState<Role>(user.role)
  const [chosen, setChosen] = useState<number[]>(user.projectIds)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setBusy(true)
    setError('')
    try {
      await updateUser(user.id, role === 'member' ? { role, projectIds: chosen } : { role })
      onSaved()
    } catch (err) {
      setError(problem(err))
      setBusy(false)
    }
  }

  return (
    <section className={`${form.card} ${styles.formCard}`} aria-labelledby="edit-user-title">
      <h2 id="edit-user-title" className={form.title}>
        Edit {user.email}
      </h2>
      <form className={styles.form} onSubmit={submit}>
        <div className={form.columns}>
          <RoleSelect value={role} onChange={setRole} />
        </div>
        {role === 'member' && <ProjectChecks projects={projects} chosen={chosen} onChange={setChosen} />}
        {error && (
          <p className={form.error} role="alert">
            {error}
          </p>
        )}
        <div className={form.buttons}>
          <button type="submit" className={`${button.button} ${button.large} ${button.primary}`} disabled={busy}>
            Save changes
          </button>
          <button type="button" className={`${button.button} ${button.large}`} onClick={onCancel}>
            Cancel
          </button>
        </div>
      </form>
    </section>
  )
}
