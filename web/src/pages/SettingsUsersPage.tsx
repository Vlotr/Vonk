import { type FormEvent, useState } from 'react'
import { useSearchParams } from 'react-router'
import {
  createInvite,
  createResetLink,
  deleteUser,
  getSettingsProjects,
  getSettingsUsers,
  type Invite,
  revokeInvite,
  type Role,
  type SettingsProject,
  type SettingsUser,
  updateUser,
} from '../api/client'
import { AdminOnly } from '../components/AdminOnly'
import button from '../components/Button.module.css'
import { CopyBlock } from '../components/CopyBlock'
import form from '../components/Form.module.css'
import { ChevronDownIcon, PlusIcon } from '../components/Icons'
import { LoadError } from '../components/LoadError'
import { SettingsHeader } from '../components/SettingsHeader'
import { Tag } from '../components/Tag'
import { useMe } from '../layout/MeContext'
import { formatFullDate } from '../lib/format'
import { expiresIn, roleLabel } from '../lib/settings'
import { problem } from '../lib/signin'
import { usePolling } from '../lib/usePolling'
import page from './Page.module.css'
import styles from './SettingsUsersPage.module.css'

/** A link just made, shown once: the server keeps only a hash of its token. */
interface ShownLink {
  title: string
  link: string
  text: string
}

// SettingsUsersPage is where admins manage who signs in: users with their
// role and projects, invites (a link to hand over, also mailed when mail
// is on), reset links for members, and removing users. ?invite opens the
// invite form, ?edit=<id> the form of one user.
export function SettingsUsersPage() {
  return (
    <AdminOnly>
      <ManageUsers />
    </AdminOnly>
  )
}

function ManageUsers() {
  const me = useMe()
  const [params, setParams] = useSearchParams()
  const users = usePolling(getSettingsUsers, 'settings-users')
  const projects = usePolling(getSettingsProjects, 'settings-projects')
  const [shown, setShown] = useState<ShownLink>()
  const [failed, setFailed] = useState('')
  const [removing, setRemoving] = useState<number>()

  const projectList = projects.data ?? []
  const editing = users.data?.users.find((u) => String(u.id) === params.get('edit'))
  const inviting = !editing && params.has('invite')
  const close = () => setParams({}, { replace: true })
  const mailNote = (mailed: boolean, who: string) =>
    mailed ? `We also mailed it to ${who}.` : me?.mailEnabled ? 'The mail could not be sent, so send the link yourself.' : 'This server does not send mail, so send the link yourself.'

  const resetLink = async (user: SettingsUser) => {
    setFailed('')
    try {
      const made = await createResetLink(user.id)
      setShown({
        title: `Reset link for ${user.email}`,
        link: made.link,
        text: `Give this link to ${user.email}. It works once, for one hour, and signs them out everywhere. ${mailNote(made.mailed, user.email)}`,
      })
      window.scrollTo({ top: 0 })
    } catch (err) {
      setFailed(problem(err))
    }
  }

  const remove = async (user: SettingsUser) => {
    setFailed('')
    try {
      await deleteUser(user.id)
      setRemoving(undefined)
      users.reload()
    } catch (err) {
      setFailed(problem(err))
    }
  }

  const revoke = async (invite: Invite) => {
    setFailed('')
    try {
      await revokeInvite(invite.id)
      users.reload()
    } catch (err) {
      setFailed(problem(err))
    }
  }

  return (
    <section>
      <SettingsHeader
        title="Manage users"
        lead="Admins manage projects and users and see every project. Members see only the projects you give them."
        actions={
          <button
            type="button"
            className={`${button.button} ${button.large} ${button.primary} ${button.withIcon}`}
            onClick={() => {
              setShown(undefined)
              setParams({ invite: '' })
            }}
          >
            <PlusIcon />
            Invite user
          </button>
        }
      />

      {shown && <LinkCard shown={shown} onDone={() => setShown(undefined)} />}
      {inviting && !shown && (
        <InviteForm
          projects={projectList}
          mailEnabled={me?.mailEnabled ?? false}
          onCancel={close}
          onInvited={(email, link, mailed) => {
            close()
            users.reload()
            setShown({
              title: `Invite for ${email}`,
              link,
              text: `Send this link to ${email}. It works once, for 7 days. ${mailNote(mailed, email)}`,
            })
          }}
        />
      )}
      {editing && (
        <EditUserForm
          key={editing.id}
          user={editing}
          projects={projectList}
          onCancel={close}
          onSaved={() => {
            close()
            users.reload()
          }}
        />
      )}
      {failed && (
        <p className={`${form.error} ${styles.failed}`} role="alert">
          {failed}
        </p>
      )}

      <section className={`${page.card} ${styles.card}`} aria-labelledby="users-title">
        <h2 id="users-title" className={`${page.cardTitle} ${styles.cardTitle}`}>
          Users
        </h2>
        {!users.data ? (
          users.error ? <LoadError /> : null
        ) : (
          <div className={page.scroll}>
            <table className={`${page.table} ${styles.table}`}>
              <thead>
                <tr>
                  <th scope="col">User</th>
                  <th scope="col" className={styles.roleColumn}>
                    Role
                  </th>
                  <th scope="col">Projects</th>
                  <th scope="col" className={page.fit}>
                    <span className={page.visuallyHidden}>Actions</span>
                  </th>
                </tr>
              </thead>
              <tbody>
                {users.data.users.map((u) => (
                  <tr key={u.id}>
                    <td>
                      <div className={styles.email}>
                        {u.email}
                        {u.id === me?.id && <Tag>You</Tag>}
                      </div>
                      <div className={styles.meta}>Joined {formatFullDate(new Date(u.createdAt))}</div>
                    </td>
                    <td>{roleLabel(u.role)}</td>
                    <td>
                      <ProjectTags role={u.role} ids={u.projectIds} projects={projectList} />
                    </td>
                    <td className={page.fit}>
                      {removing === u.id ? (
                        <div className={styles.actions}>
                          <span className={styles.ask}>Remove {u.email}?</span>
                          <button type="button" className={`${button.button} ${button.danger}`} onClick={() => remove(u)}>
                            Remove
                          </button>
                          <button type="button" className={button.button} onClick={() => setRemoving(undefined)}>
                            Cancel
                          </button>
                        </div>
                      ) : (
                        <div className={styles.actions}>
                          <button type="button" className={button.button} onClick={() => setParams({ edit: String(u.id) })}>
                            Edit
                          </button>
                          {u.role === 'member' && (
                            <button type="button" className={button.button} onClick={() => resetLink(u)}>
                              Reset password
                            </button>
                          )}
                          {u.id !== me?.id && (
                            <button type="button" className={button.button} onClick={() => setRemoving(u.id)}>
                              Remove
                            </button>
                          )}
                        </div>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </section>

      {users.data && users.data.invites.length > 0 && (
        <section className={`${page.card} ${styles.card}`} aria-labelledby="invites-title">
          <h2 id="invites-title" className={`${page.cardTitle} ${styles.cardTitle}`}>
            Pending invites
          </h2>
          <div className={page.scroll}>
            <table className={`${page.table} ${styles.table}`}>
              <thead>
                <tr>
                  <th scope="col">Email</th>
                  <th scope="col" className={styles.roleColumn}>
                    Role
                  </th>
                  <th scope="col">Projects</th>
                  <th scope="col">Expires</th>
                  <th scope="col" className={page.fit}>
                    <span className={page.visuallyHidden}>Actions</span>
                  </th>
                </tr>
              </thead>
              <tbody>
                {users.data.invites.map((inv) => (
                  <tr key={inv.id}>
                    <td>
                      <div className={styles.email}>{inv.email}</div>
                    </td>
                    <td>{roleLabel(inv.role)}</td>
                    <td>
                      <ProjectTags role={inv.role} ids={inv.projectIds} projects={projectList} />
                    </td>
                    <td className={styles.meta}>{expiresIn(new Date(inv.expiresAt), new Date())}</td>
                    <td className={page.fit}>
                      <button type="button" className={button.button} onClick={() => revoke(inv)}>
                        Revoke
                      </button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </section>
      )}
    </section>
  )
}

/** ProjectTags lists the projects a member sees; admins see them all. */
function ProjectTags({ role, ids, projects }: { role: Role; ids: number[]; projects: SettingsProject[] }) {
  if (role === 'admin') return <span className={styles.meta}>All projects</span>
  const names = projects.filter((p) => ids.includes(p.id)).map((p) => p.name)
  if (names.length === 0) return <span className={styles.meta}>No projects yet</span>
  return (
    <div className={styles.tags}>
      {names.map((name) => (
        <Tag key={name}>{name}</Tag>
      ))}
    </div>
  )
}

/** LinkCard shows a link that was just made, with a Copy button. */
function LinkCard({ shown, onDone }: { shown: ShownLink; onDone: () => void }) {
  return (
    <section className={`${form.card} ${styles.formCard}`} aria-labelledby="link-title">
      <h2 id="link-title" className={form.title}>
        {shown.title}
      </h2>
      <div className={form.stack}>
        <CopyBlock label="Link" value={shown.link} />
        <p className={form.note}>{shown.text} The link is shown only now.</p>
        <div className={form.buttons}>
          <button type="button" className={`${button.button} ${button.large}`} onClick={onDone}>
            Done
          </button>
        </div>
      </div>
    </section>
  )
}

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

function InviteForm({ projects, mailEnabled, onCancel, onInvited }: InviteFormProps) {
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

function EditUserForm({ user, projects, onCancel, onSaved }: EditUserFormProps) {
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
