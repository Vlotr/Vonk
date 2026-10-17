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
import { browserSnippet, identifySnippet, LARAVEL_USER_SNIPPET, laravelEnvLine, posthogSnippet, rateLimitSentence } from '../lib/settings'
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
// the steps to send errors from Laravel and the browser, the posthog-js
// setup for page views, and how to give both SDKs the same user id.
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
        <p className={form.note}>
          {rateLimitSentence(project.rateLimitPerMinute)}{' '}
          <Link to={`/settings/projects?edit=${project.id}`}>Change the limit under Edit project.</Link>
        </p>
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
          <p className={form.note}>
            A production build sends minified code. Upload its source maps with an{' '}
            <Link to="/settings/api-tokens?new">API token</Link> (sentry-cli or @sentry/vite-plugin), and its errors show your own files
            and lines.
          </p>
        </section>
      )}

      <PageViewsCard project={project} onChange={setProject} />
      {project.tracksPageviews && <SameUserCard project={project} />}

      {untracked.length > 0 && (
        <p className={`${form.note} ${styles.untracked}`}>
          Not tracked: {untracked.join(' and ')}. <Link to={`/settings/projects?edit=${project.id}`}>Change it under Edit project.</Link>
        </p>
      )}
    </section>
  )
}

/**
 * PageViewsCard shows the PostHog token and the posthog-js setup this tool
 * was tested with, or offers to turn page views on.
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
          <p className={form.note}>
            The PostHog SDK sends page views with this token, to this server. They show up under Pages a minute or two later.
          </p>
          <div className={form.field}>
            <span className={form.label}>Project token</span>
            <CopyBlock label="PostHog token" value={project.posthogToken} />
          </div>
          <div className={form.field}>
            <span className={form.label}>API host</span>
            <CopyBlock label="PostHog host" value={project.posthogHost} />
          </div>
          <ol className={styles.steps}>
            <li>
              <p>Install the PostHog SDK for the browser:</p>
              <CopyBlock label="Install command" value="npm install posthog-js" />
            </li>
            <li>
              <p>Start it on every page, as early as possible:</p>
              <CopyBlock label="PostHog setup" value={posthogSnippet(project.posthogToken, project.posthogHost)} />
            </li>
          </ol>
          <p className={form.note}>
            It sets no cookies and stores nothing in the browser (<code>persistence: &apos;memory&apos;</code>), so it needs no cookie
            banner. The price: a visitor who is not signed in counts as a new visitor on every page load. posthog-js warns about this in the
            browser console (&ldquo;PostHog will mint a new distinct ID on every page load&rdquo;, with advice about PostHog&apos;s person
            pages); this tool has no such limit, so the warning can be ignored. A page view ends when the visitor leaves the page or opens
            the next one, which gives the time on the page. Session recording stays off.
          </p>
        </>
      ) : (
        <>
          <p className={form.note}>
            Page views are off for this project. Events your server sends with a PostHog SDK (such as posthog-node) still show under
            Events; they need this token and host:
          </p>
          <div className={form.field}>
            <span className={form.label}>Project token</span>
            <CopyBlock label="PostHog token" value={project.posthogToken} />
          </div>
          <div className={form.field}>
            <span className={form.label}>API host</span>
            <CopyBlock label="PostHog host" value={project.posthogHost} />
          </div>
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

/**
 * SameUserCard explains how to give the Sentry and PostHog SDKs the same
 * user id, so an issue's users and their page views are the same people.
 * It shows for projects that track page views; the Laravel step for those
 * that track Laravel errors.
 */
function SameUserCard({ project }: { project: ProjectKeys }) {
  return (
    <section className={`${page.card} ${styles.card}`} aria-labelledby="same-user-title">
      <h2 id="same-user-title" className={page.cardTitle}>
        The same user in errors and page views
      </h2>
      <p className={form.note}>
        Give both SDKs your app&apos;s id of the signed-in user. Then the user of an error and the visitor of a page are the same person, and
        signed-in visitors count once however many pages they open. Send the id only, never an email address.
      </p>
      <ol className={styles.steps}>
        <li>
          <p>In the browser, where your app knows who signed in:</p>
          <CopyBlock label="Browser user id" value={identifySnippet()} />
        </li>
        {project.tracksLaravel && (
          <li>
            <p>
              In Laravel, in <code>app/Providers/AppServiceProvider.php</code> (the <code>use</code> lines at the top, the listener in{' '}
              <code>boot()</code>):
            </p>
            <CopyBlock label="Laravel user id" value={LARAVEL_USER_SNIPPET} />
          </li>
        )}
      </ol>
    </section>
  )
}
