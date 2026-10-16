import { type FormEvent, type ReactNode, useState } from 'react'
import { Link, useNavigate, useSearchParams } from 'react-router'
import {
  createProject,
  deleteProject,
  getSettingsProjects,
  type ProjectSettings,
  rotateKeys,
  type SettingsProject,
  updateProject,
} from '../api/client'
import { AdminOnly } from '../components/AdminOnly'
import button from '../components/Button.module.css'
import form from '../components/Form.module.css'
import { ChevronDownIcon, PlusIcon } from '../components/Icons'
import { LoadError } from '../components/LoadError'
import { SettingsHeader } from '../components/SettingsHeader'
import { Tag } from '../components/Tag'
import { useProjects } from '../layout/ProjectsContext'
import { formatFullDate } from '../lib/format'
import { parsePatterns, patternsText, RETENTION_CHOICES, retentionLabel, trackingLabels } from '../lib/settings'
import { problem } from '../lib/signin'
import { usePolling } from '../lib/usePolling'
import page from './Page.module.css'
import { RoutePatternsField } from './RoutePatternsField'
import styles from './SettingsProjectsPage.module.css'

/** NEW_PROJECT is how a new project starts out, as the design shows it. */
const NEW_PROJECT: ProjectSettings = { name: '', retentionDays: 90, tracksLaravel: true, tracksBrowser: true, tracksPageviews: false }

// SettingsProjectsPage is Settings.png: "Manage projects", the New project
// form (Add project), and "Your projects" with what each tracks and keeps.
// Edit opens the same form for one project, with its keys and delete.
// ?new opens the New project form, ?edit=<id> the form of one project.
export function SettingsProjectsPage() {
  return (
    <AdminOnly>
      <ManageProjects />
    </AdminOnly>
  )
}

function ManageProjects() {
  const navigate = useNavigate()
  const [params, setParams] = useSearchParams()
  const list = usePolling(getSettingsProjects, 'settings-projects')
  const sidebar = useProjects()
  const projects = list.data

  const editing = projects?.find((p) => String(p.id) === params.get('edit'))
  const adding = !editing && (params.has('new') || projects?.length === 0)
  const close = () => setParams({}, { replace: true })
  const changed = () => {
    list.reload()
    sidebar.reload() // names and the project list in the sidebar
  }

  const create = async (settings: ProjectSettings) => {
    const created = await createProject(settings)
    changed()
    navigate(`/settings/projects/${created.id}/setup`, { state: { created: true } })
  }

  const addPageViews = async (project: SettingsProject) => {
    // If this fails, the setup page says page views are off and offers to turn them on.
    await updateProject(project.id, { tracksPageviews: true }).then(changed, () => {})
    navigate(`/settings/projects/${project.id}/setup#page-views`)
  }

  return (
    <section>
      <SettingsHeader
        title="Manage projects"
        lead="Each project is one app. It gets its own keys for the Sentry and PostHog SDKs."
        actions={
          <button type="button" className={`${button.button} ${button.large} ${button.primary} ${button.withIcon}`} onClick={() => setParams({ new: '' })}>
            <PlusIcon />
            Add project
          </button>
        }
      />

      {adding && (
        <ProjectForm
          key="new"
          title="New project"
          initial={NEW_PROJECT}
          note="After you create the project, you get the keys and the setup steps."
          submitLabel="Create project"
          onSubmit={create}
          onCancel={projects?.length === 0 ? undefined : close}
        />
      )}
      {editing && (
        <ProjectForm
          key={editing.id}
          title={`Edit ${editing.name}`}
          initial={editing}
          routePatterns={{ projectId: editing.id, initial: editing.routePatterns }}
          submitLabel="Save changes"
          onSubmit={async (settings, routePatterns) => {
            await updateProject(editing.id, { ...settings, routePatterns })
            changed()
            close()
          }}
          onCancel={close}
        >
          <KeysSection project={editing} />
          <DeleteSection
            project={editing}
            onDeleted={() => {
              changed()
              close()
            }}
          />
        </ProjectForm>
      )}

      <section className={`${page.card} ${styles.list}`} aria-labelledby="your-projects">
        <h2 id="your-projects" className={`${page.cardTitle} ${styles.listTitle}`}>
          Your projects
        </h2>
        {!projects ? (
          list.error ? <LoadError /> : null
        ) : projects.length === 0 ? (
          <p className={page.empty}>No projects yet. Create the first one above.</p>
        ) : (
          <div className={page.scroll}>
            <table className={`${page.table} ${styles.table}`}>
              <thead>
                <tr>
                  <th scope="col" className={styles.projectColumn}>
                    Project
                  </th>
                  <th scope="col">Tracks</th>
                  <th scope="col" className={styles.keepsColumn}>
                    Keeps data for
                  </th>
                  <th scope="col" className={page.fit}>
                    <span className={page.visuallyHidden}>Actions</span>
                  </th>
                </tr>
              </thead>
              <tbody>
                {projects.map((p) => (
                  <tr key={p.id}>
                    <td>
                      <div className={styles.name}>{p.name}</div>
                      <div className={styles.created}>Created {formatFullDate(new Date(p.createdAt))}</div>
                    </td>
                    <td>
                      <div className={styles.tracks}>
                        {trackingLabels(p).map((label) => (
                          <Tag key={label}>{label}</Tag>
                        ))}
                        {!p.tracksPageviews && (
                          <button type="button" className={styles.addChip} onClick={() => addPageViews(p)}>
                            Add page views
                          </button>
                        )}
                      </div>
                    </td>
                    <td className={styles.keeps}>{retentionLabel(p.retentionDays)}</td>
                    <td className={page.fit}>
                      <div className={styles.actions}>
                        <Link to={`/settings/projects/${p.id}/setup`} className={`${button.button}`}>
                          Setup and keys
                        </Link>
                        <button type="button" className={`${button.button}`} onClick={() => setParams({ edit: String(p.id) })}>
                          Edit
                        </button>
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </section>
    </section>
  )
}

/** retentionValue is the select's value for a retention: the days, or "forever". */
function retentionValue(days: number | null): string {
  return days === null ? 'forever' : String(days)
}

interface ProjectFormProps {
  title: string
  initial: ProjectSettings
  /** routePatterns adds the route patterns field (Edit project only). */
  routePatterns?: { projectId: number; initial: string[] }
  note?: string
  submitLabel: string
  /** onSubmit gets the route patterns too when the form has them. */
  onSubmit: (settings: ProjectSettings, routePatterns?: string[]) => Promise<void>
  /** onCancel closes the form; without it there is no Cancel button. */
  onCancel?: () => void
  children?: ReactNode
}

/** ProjectForm is the New project card: name, how long to keep data, what to track (and, when editing, the route patterns). */
function ProjectForm({ title, initial, routePatterns, note, submitLabel, onSubmit, onCancel, children }: ProjectFormProps) {
  const [settings, setSettings] = useState<ProjectSettings>(initial)
  const [patterns, setPatterns] = useState(patternsText(routePatterns?.initial ?? []))
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const set = (change: Partial<ProjectSettings>) => setSettings((s) => ({ ...s, ...change }))

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (!settings.name.trim()) {
      setError('Give the project a name.')
      return
    }
    setBusy(true)
    setError('')
    try {
      await onSubmit({ ...settings, name: settings.name.trim() }, routePatterns ? parsePatterns(patterns) : undefined)
    } catch (err) {
      setError(problem(err))
    } finally {
      setBusy(false)
    }
  }

  const check = (field: 'tracksLaravel' | 'tracksBrowser' | 'tracksPageviews', label: string, sdk: string) => (
    <label className={form.check}>
      <input type="checkbox" checked={settings[field]} onChange={(e) => set({ [field]: e.target.checked })} />
      <span>
        {label}
        <span className={form.sdk}>({sdk})</span>
      </span>
    </label>
  )

  return (
    <section className={`${form.card} ${styles.formCard}`} aria-labelledby="project-form-title">
      <h2 id="project-form-title" className={form.title}>
        {title}
      </h2>
      <form onSubmit={submit}>
        <div className={form.columns}>
          <label className={form.field}>
            <span className={form.label}>Project name</span>
            <input
              className={form.input}
              placeholder="For example: Webshop"
              maxLength={100}
              value={settings.name}
              onChange={(e) => set({ name: e.target.value })}
              autoFocus
            />
          </label>
          <label className={form.field}>
            <span className={form.label}>Keep data for</span>
            <span className={form.selectBox}>
              <select
                className={form.select}
                value={retentionValue(settings.retentionDays)}
                onChange={(e) => set({ retentionDays: e.target.value === 'forever' ? null : Number(e.target.value) })}
              >
                {RETENTION_CHOICES.map((c) => (
                  <option key={c.label} value={retentionValue(c.days)}>
                    {c.label}
                  </option>
                ))}
              </select>
              <ChevronDownIcon className={form.chevron} />
            </span>
          </label>
        </div>

        <fieldset className={`${form.fieldset} ${styles.tracking}`}>
          <legend className={`${form.label} ${form.legend}`}>What do you want to track?</legend>
          <div className={form.checks}>
            {check('tracksLaravel', 'Errors from Laravel', 'Sentry SDK')}
            {check('tracksBrowser', 'Errors from the browser', 'Sentry SDK')}
            {check('tracksPageviews', 'Page views and events', 'PostHog SDK')}
          </div>
        </fieldset>

        {routePatterns && <RoutePatternsField projectId={routePatterns.projectId} text={patterns} onChange={setPatterns} />}

        {note && <p className={`${form.note} ${styles.note}`}>{note}</p>}
        {error && (
          <p className={`${form.error} ${styles.note}`} role="alert">
            {error}
          </p>
        )}
        <div className={`${form.buttons} ${styles.buttons}`}>
          <button type="submit" className={`${button.button} ${button.large} ${button.primary}`} disabled={busy}>
            {submitLabel}
          </button>
          {onCancel && (
            <button type="button" className={`${button.button} ${button.large}`} onClick={onCancel}>
              Cancel
            </button>
          )}
        </div>
      </form>
      {children}
    </section>
  )
}

/** KeysSection rotates a project's DSN key and PostHog token, after asking. */
function KeysSection({ project }: { project: SettingsProject }) {
  const [asking, setAsking] = useState(false)
  const [done, setDone] = useState(false)
  const [error, setError] = useState('')

  const rotate = async () => {
    setError('')
    try {
      await rotateKeys(project.id)
      setDone(true)
      setAsking(false)
    } catch (err) {
      setError(problem(err))
    }
  }

  return (
    <section className={styles.extra} aria-labelledby="keys-title">
      <h3 id="keys-title" className={form.label}>
        Keys
      </h3>
      {done ? (
        <p className={form.success}>
          New keys are ready, and the old ones no longer work. <Link to={`/settings/projects/${project.id}/setup`}>See them under Setup and keys.</Link>
        </p>
      ) : asking ? (
        <>
          <p className={form.note}>
            Make new keys for {project.name}? The old DSN and token stop working at once: apps send nothing until they have the new ones.
          </p>
          <div className={form.buttons}>
            <button type="button" className={`${button.button} ${button.primary}`} onClick={rotate}>
              Make new keys
            </button>
            <button type="button" className={button.button} onClick={() => setAsking(false)}>
              Cancel
            </button>
          </div>
        </>
      ) : (
        <>
          <p className={form.note}>Got the DSN or the PostHog token out where it should not be? Make new ones.</p>
          <div className={form.buttons}>
            <button type="button" className={button.button} onClick={() => setAsking(true)}>
              Rotate keys
            </button>
          </div>
        </>
      )}
      {error && (
        <p className={form.error} role="alert">
          {error}
        </p>
      )}
    </section>
  )
}

/** DeleteSection deletes a project once its name is typed. */
function DeleteSection({ project, onDeleted }: { project: SettingsProject; onDeleted: () => void }) {
  const [typed, setTyped] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  const remove = async () => {
    setBusy(true)
    setError('')
    try {
      await deleteProject(project.id)
      onDeleted()
    } catch (err) {
      setError(problem(err))
      setBusy(false)
    }
  }

  return (
    <section className={styles.extra} aria-labelledby="delete-title">
      <h3 id="delete-title" className={form.label}>
        Delete project
      </h3>
      <p className={form.note}>This removes {project.name} with all its issues and events. It cannot be undone.</p>
      <label className={`${form.field} ${styles.confirm}`}>
        <span className={form.hint}>Type {project.name} to confirm</span>
        <input className={form.input} value={typed} onChange={(e) => setTyped(e.target.value)} autoComplete="off" />
      </label>
      <div className={form.buttons}>
        <button type="button" className={`${button.button} ${button.danger}`} disabled={typed !== project.name || busy} onClick={remove}>
          Delete project
        </button>
      </div>
      {error && (
        <p className={form.error} role="alert">
          {error}
        </p>
      )}
    </section>
  )
}
