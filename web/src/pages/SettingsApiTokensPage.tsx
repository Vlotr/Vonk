import { type FormEvent, useState } from 'react'
import { useSearchParams } from 'react-router'
import { type APIToken, createAPIToken, deleteAPIToken, getAPITokens, type Project } from '../api/client'
import { AdminOnly } from '../components/AdminOnly'
import button from '../components/Button.module.css'
import { CopyBlock } from '../components/CopyBlock'
import form from '../components/Form.module.css'
import { PlusIcon } from '../components/Icons'
import { LoadError } from '../components/LoadError'
import { SettingsHeader } from '../components/SettingsHeader'
import { Tag } from '../components/Tag'
import { useProjects } from '../layout/ProjectsContext'
import { buildEnv, cliCommands, lastUsedText, tokenHint, viteSnippet } from '../lib/apiTokens'
import { formatFullDate } from '../lib/format'
import { problem } from '../lib/signin'
import { usePolling } from '../lib/usePolling'
import page from './Page.module.css'
import styles from './SettingsApiTokensPage.module.css'

// SettingsApiTokensPage is where admins make the tokens sentry-cli and
// @sentry/vite-plugin upload source maps with, so minified browser errors
// show the original code. A token uploads only to the projects it was made
// for, is shown once (the server keeps a hash) with the setup for both
// tools, and can be revoked. ?new opens the form. There is no design for
// this page; it follows Settings → Users.
export function SettingsApiTokensPage() {
  return (
    <AdminOnly>
      <ManageTokens />
    </AdminOnly>
  )
}

/** Made is a token just made, shown this once. */
interface Made {
  token: APIToken
  secret: string
  url: string
}

function ManageTokens() {
  const [params, setParams] = useSearchParams()
  const tokens = usePolling(getAPITokens, 'api-tokens')
  const projects = useProjects().data ?? []
  const [made, setMade] = useState<Made>()
  const [failed, setFailed] = useState('')
  const [revoking, setRevoking] = useState<number>()

  const revoke = async (token: APIToken) => {
    setFailed('')
    try {
      await deleteAPIToken(token.id)
      setRevoking(undefined)
      tokens.reload()
    } catch (err) {
      setFailed(problem(err))
    }
  }

  return (
    <section>
      <SettingsHeader
        title="API tokens"
        lead="sentry-cli and @sentry/vite-plugin upload your source maps with a token, so errors from minified code show your original files and lines. Each token uploads only to the projects you choose."
        actions={
          <button
            type="button"
            className={`${button.button} ${button.large} ${button.primary} ${button.withIcon}`}
            onClick={() => {
              setMade(undefined)
              setParams({ new: '' })
            }}
          >
            <PlusIcon />
            New token
          </button>
        }
      />

      {made && <MadeCard made={made} projects={projects} onDone={() => setMade(undefined)} />}
      {params.has('new') && !made && (
        <TokenForm
          projects={projects}
          onCancel={() => setParams({}, { replace: true })}
          onMade={(m) => {
            setParams({}, { replace: true })
            setMade(m)
            tokens.reload()
            window.scrollTo({ top: 0 })
          }}
        />
      )}
      {failed && (
        <p className={`${form.error} ${styles.failed}`} role="alert">
          {failed}
        </p>
      )}

      <section className={`${page.card} ${styles.card}`} aria-labelledby="tokens-title">
        <h2 id="tokens-title" className={`${page.cardTitle} ${styles.cardTitle}`}>
          Tokens
        </h2>
        {!tokens.data ? (
          tokens.error ? (
            <LoadError />
          ) : null
        ) : tokens.data.tokens.length === 0 ? (
          <p className={`${page.empty} ${styles.empty}`}>
            No tokens yet. Make one for the build that deploys your site, so its errors show your own code.
          </p>
        ) : (
          <div className={page.scroll}>
            <table className={`${page.table} ${styles.table}`}>
              <thead>
                <tr>
                  <th scope="col">Token</th>
                  <th scope="col">Uploads to</th>
                  <th scope="col" className={styles.usedColumn}>
                    Last used
                  </th>
                  <th scope="col" className={page.fit}>
                    <span className={page.visuallyHidden}>Actions</span>
                  </th>
                </tr>
              </thead>
              <tbody>
                {tokens.data.tokens.map((t) => (
                  <tr key={t.id}>
                    <td>
                      <div className={styles.name}>{t.name}</div>
                      <div className={styles.meta}>
                        <code>{tokenHint(t.hint)}</code> · Made {formatFullDate(new Date(t.createdAt))}
                      </div>
                    </td>
                    <td>
                      <ProjectTags ids={t.projectIds} projects={projects} />
                    </td>
                    <td className={styles.meta}>{lastUsedText(t.lastUsedAt, new Date())}</td>
                    <td className={page.fit}>
                      {revoking === t.id ? (
                        <div className={styles.actions}>
                          <span className={styles.ask}>Uploads with {t.name} stop working.</span>
                          <button type="button" className={`${button.button} ${button.danger}`} onClick={() => revoke(t)}>
                            Revoke
                          </button>
                          <button type="button" className={button.button} onClick={() => setRevoking(undefined)}>
                            Cancel
                          </button>
                        </div>
                      ) : (
                        <div className={styles.actions}>
                          <button type="button" className={button.button} onClick={() => setRevoking(t.id)}>
                            Revoke
                          </button>
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
    </section>
  )
}

/** ProjectTags names a token's projects, with the slug the tools use as a tooltip. */
function ProjectTags({ ids, projects }: { ids: number[]; projects: Project[] }) {
  const mine = projects.filter((p) => ids.includes(p.id))
  if (mine.length === 0) return <span className={styles.meta}>No projects any more</span>
  return (
    <div className={styles.tags}>
      {mine.map((p) => (
        <span key={p.id} title={`Slug: ${p.slug}`}>
          <Tag>{p.name}</Tag>
        </span>
      ))}
    </div>
  )
}

/** TokenForm makes a token: a name to recognize it by and its projects. */
function TokenForm({ projects, onCancel, onMade }: { projects: Project[]; onCancel: () => void; onMade: (m: Made) => void }) {
  const [name, setName] = useState('')
  const [chosen, setChosen] = useState<number[]>(projects.length === 1 ? [projects[0].id] : [])
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setBusy(true)
    setError('')
    try {
      onMade(await createAPIToken(name.trim(), chosen))
    } catch (err) {
      setError(problem(err))
      setBusy(false)
    }
  }

  return (
    <section className={`${form.card} ${styles.formCard}`} aria-labelledby="token-form-title">
      <h2 id="token-form-title" className={form.title}>
        New token
      </h2>
      <form className={styles.form} onSubmit={submit}>
        <label className={form.field}>
          <span className={form.label}>Name</span>
          <input
            className={form.input}
            placeholder="For example: Deploys of the webshop"
            required
            maxLength={100}
            value={name}
            onChange={(e) => setName(e.target.value)}
            autoFocus
          />
        </label>
        <fieldset className={form.fieldset}>
          <legend className={`${form.label} ${form.legend}`}>Projects it uploads to</legend>
          {projects.length === 0 ? (
            <p className={form.note}>There are no projects yet.</p>
          ) : (
            <div className={`${form.checks} ${styles.checks}`}>
              {projects.map((p) => (
                <label key={p.id} className={form.check}>
                  <input
                    type="checkbox"
                    checked={chosen.includes(p.id)}
                    onChange={(e) => setChosen(e.target.checked ? [...chosen, p.id] : chosen.filter((id) => id !== p.id))}
                  />
                  <span>{p.name}</span>
                </label>
              ))}
            </div>
          )}
        </fieldset>
        <p className={form.note}>You see the token once, with the setup for your build. Keep it as a secret of your build, not in your code.</p>
        {error && (
          <p className={form.error} role="alert">
            {error}
          </p>
        )}
        <div className={form.buttons}>
          <button type="submit" className={`${button.button} ${button.large} ${button.primary}`} disabled={busy}>
            Make token
          </button>
          <button type="button" className={`${button.button} ${button.large}`} onClick={onCancel}>
            Cancel
          </button>
        </div>
      </form>
    </section>
  )
}

/** MadeCard shows a new token once, with how to use it in a Vite build or with sentry-cli. */
function MadeCard({ made, projects, onDone }: { made: Made; projects: Project[]; onDone: () => void }) {
  const mine = projects.filter((p) => made.token.projectIds.includes(p.id))
  const slug = mine[0]?.slug ?? 'your-project'
  const slugs = mine.map((p) => p.slug).join(' or ')
  return (
    <section className={`${form.card} ${styles.formCard}`} aria-labelledby="made-title">
      <h2 id="made-title" className={form.title}>
        Token {made.token.name}
      </h2>
      <div className={form.stack}>
        <CopyBlock label="Token" value={made.secret} />
        <p className={form.note}>
          Copy it now: it is shown only this once. Give it to your build as a secret, in the environment:
        </p>
        <CopyBlock label="Build environment" value={buildEnv(made.secret)} />
        <h3 className={styles.subtitle}>With @sentry/vite-plugin</h3>
        <p className={form.note}>
          Install it with <code>npm install --save-dev @sentry/vite-plugin</code> and add it to <code>vite.config.ts</code>
          {mine.length > 1 ? `; project is the slug of the project the build is for (${slugs})` : ''}:
        </p>
        <CopyBlock label="Vite setup" value={viteSnippet(made.url, slug)} />
        <h3 className={styles.subtitle}>With sentry-cli</h3>
        <p className={form.note}>After the build, with the token in the environment (the token tells sentry-cli where this server is):</p>
        <CopyBlock label="sentry-cli commands" value={cliCommands(slug)} />
        <p className={form.note}>
          Errors that come in after the upload show the original files and lines; older errors of that build too, on their issue page.
        </p>
        <div className={form.buttons}>
          <button type="button" className={`${button.button} ${button.large}`} onClick={onDone}>
            Done
          </button>
        </div>
      </div>
    </section>
  )
}
