import { useEffect, useState } from 'react'
import { Link, useLocation, useParams } from 'react-router'
import { ApiError, getProjectKeys, type ProjectKeys, updateProject } from '../api/client'
import { AdminOnly } from '../components/AdminOnly'
import button from '../components/Button.module.css'
import { CopyBlock } from '../components/CopyBlock'
import form from '../components/Form.module.css'
import { LoadError } from '../components/LoadError'
import { SettingsHeader } from '../components/SettingsHeader'
import { useProjects } from '../layout/ProjectsContext'
import { browserSnippet, laravelEnvLine } from '../lib/settings'
import { problem } from '../lib/signin'
import { NotFoundPage } from './NotFoundPage'
import page from './Page.module.css'
import styles from './ProjectSetupPage.module.css'

/** LARAVEL_BOOTSTRAP reports a Laravel 11+ app's exceptions to Sentry, here: this tool. */
const LARAVEL_BOOTSTRAP = `use Sentry\\Laravel\\Integration;

->withExceptions(function (Exceptions $exceptions) {
    Integration::handles($exceptions);
})`

// ProjectSetupPage is "Setup and keys": the project's DSN and PostHog token,
// and the steps to send errors from Laravel and the browser. The PostHog
// steps for page views follow when page views are collected.
export function ProjectSetupPage() {
  return (
    <AdminOnly>
      <SetupAndKeys />
    </AdminOnly>
  )
}

function SetupAndKeys() {
  const projectId = Number(useParams().id)
  const location = useLocation()
  const justCreated = (location.state as { created?: boolean } | null)?.created === true
  const [project, setProject] = useState<ProjectKeys>()
  const [error, setError] = useState<unknown>()

  useEffect(() => {
    const controller = new AbortController()
    getProjectKeys(projectId, controller.signal).then(setProject, (err: unknown) => {
      if (!controller.signal.aborted) setError(err)
    })
    return () => controller.abort()
  }, [projectId])

  // "Add page views" links to the page views card.
  useEffect(() => {
    if (project && location.hash === '#page-views') document.getElementById('page-views')?.scrollIntoView()
  }, [project, location.hash])

  if (error instanceof ApiError && error.status === 404) return <NotFoundPage what="Project" />
  if (!project) return <section aria-busy={!error}>{error ? <LoadError /> : null}</section>

  const untracked = [!project.tracksLaravel && 'Laravel errors', !project.tracksBrowser && 'Browser errors'].filter(Boolean)
  return (
    <section>
      <SettingsHeader
        title={`Set up ${project.name}`}
        lead={
          justCreated
            ? `${project.name} is ready. Send it errors with the steps below; they show up under Issues within a minute.`
            : `These keys belong to ${project.name} only. Errors show up under Issues within a minute after your app sends them.`
        }
        actions={
          <Link to={`/settings/projects?edit=${project.id}`} className={`${button.button} ${button.large}`}>
            Edit project
          </Link>
        }
      />

      <section className={`${page.card} ${styles.card}`} aria-labelledby="dsn-title">
        <h2 id="dsn-title" className={page.cardTitle}>
          DSN
        </h2>
        <p className={form.note}>The Sentry SDKs send errors to this address. It is not a secret: browsers see it too.</p>
        <CopyBlock label="DSN" value={project.dsn} />
      </section>

      {project.tracksLaravel && (
        <section className={`${page.card} ${styles.card}`} aria-labelledby="laravel-title">
          <h2 id="laravel-title" className={page.cardTitle}>
            Errors from Laravel
          </h2>
          <ol className={styles.steps}>
            <li>
              <p>Install the Sentry SDK for Laravel:</p>
              <CopyBlock label="Install command" value="composer require sentry/sentry-laravel" />
            </li>
            <li>
              <p>
                Report exceptions in <code>bootstrap/app.php</code>:
              </p>
              <CopyBlock label="Exception handler" value={LARAVEL_BOOTSTRAP} />
            </li>
            <li>
              <p>
                Add the DSN to <code>.env</code>:
              </p>
              <CopyBlock label=".env line" value={laravelEnvLine(project.dsn)} />
            </li>
            <li>
              <p>Send a test error:</p>
              <CopyBlock label="Test command" value="php artisan sentry:test" />
            </li>
          </ol>
        </section>
      )}

      {project.tracksBrowser && (
        <section className={`${page.card} ${styles.card}`} aria-labelledby="browser-title">
          <h2 id="browser-title" className={page.cardTitle}>
            Errors from the browser
          </h2>
          <ol className={styles.steps}>
            <li>
              <p>Install the Sentry SDK for the browser:</p>
              <CopyBlock label="Install command" value="npm install @sentry/browser" />
            </li>
            <li>
              <p>Start it as early as possible in your app:</p>
              <CopyBlock label="Browser setup" value={browserSnippet(project.dsn)} />
            </li>
          </ol>
        </section>
      )}

      <PageViewsCard project={project} onChange={setProject} />

      {untracked.length > 0 && (
        <p className={`${form.note} ${styles.untracked}`}>
          Not tracked: {untracked.join(' and ')}. <Link to={`/settings/projects?edit=${project.id}`}>Change it under Edit project.</Link>
        </p>
      )}
    </section>
  )
}

/**
 * PageViewsCard shows the PostHog token. Page views are not collected yet;
 * the posthog-js steps arrive with them.
 */
function PageViewsCard({ project, onChange }: { project: ProjectKeys; onChange: (p: ProjectKeys) => void }) {
  const sidebar = useProjects()
  const [error, setError] = useState('')
  const turnOn = async () => {
    setError('')
    try {
      onChange(await updateProject(project.id, { tracksPageviews: true }))
      sidebar.reload()
    } catch (err) {
      setError(problem(err))
    }
  }

  return (
    <section id="page-views" className={`${page.card} ${styles.card}`} aria-labelledby="pageviews-title">
      <h2 id="pageviews-title" className={page.cardTitle}>
        Page views and events
      </h2>
      {project.tracksPageviews ? (
        <>
          <p className={form.note}>The PostHog SDK sends page views with this token, to this server.</p>
          <div className={form.field}>
            <span className={form.label}>Project token</span>
            <CopyBlock label="PostHog token" value={project.posthogToken} />
          </div>
          <div className={form.field}>
            <span className={form.label}>API host</span>
            <CopyBlock label="PostHog host" value={project.posthogHost} />
          </div>
          <p className={form.note}>
            Page views are not collected yet. When they are, the steps to set up posthog-js appear here; the token and host stay the same.
          </p>
        </>
      ) : (
        <>
          <p className={form.note}>Page views are off for this project.</p>
          <div className={form.buttons}>
            <button type="button" className={button.button} onClick={turnOn}>
              Turn on page views
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
