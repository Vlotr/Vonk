import { type ReactNode, useState } from 'react'
import { Link, useParams } from 'react-router'
import {
  type AlertChannels,
  type AlertKinds,
  type AlertsChange,
  ApiError,
  type DigestFrequency,
  getProjectAlerts,
  type ProjectAlerts,
  testProjectSlack,
  updateProjectAlerts,
} from '../api/client'
import form from '../components/Form.module.css'
import { ChevronDownIcon } from '../components/Icons'
import { LoadError } from '../components/LoadError'
import { SlackWebhook } from '../components/SlackWebhook'
import { useMe } from '../layout/MeContext'
import { alertsHeadline, alertsLead, MAX_THRESHOLD, MIN_THRESHOLD, parseThreshold, withAlertsChange } from '../lib/alerts'
import { problem } from '../lib/signin'
import { usePolling } from '../lib/usePolling'
import styles from './AlertsPage.module.css'
import { NotFoundPage } from './NotFoundPage'
import page from './Page.module.css'

const FREQUENCIES: { value: DigestFrequency; label: string }[] = [
  { value: 'daily', label: 'Daily' },
  { value: 'weekly', label: 'Weekly' },
  { value: 'monthly', label: 'Monthly' },
]

// AlertsPage is a project's notifications: which kinds go out by email
// and to Slack, the spike threshold, how often the summary comes, and the
// Slack webhook. Everyone who sees the project reads them; admins change
// them (ruling 35), and a change saves at once. Members see the same page
// with every control disabled and the webhook masked. The page waits for
// the signed-in user (as AdminOnly does), so an admin never sees it read
// only first.
export function AlertsPage() {
  const projectId = Number(useParams().id)
  const polled = usePolling((signal) => getProjectAlerts(projectId, signal), `alerts-${projectId}`)
  const [failed, setFailed] = useState('')
  const me = useMe()
  const canChange = me?.role === 'admin'

  const change = async (c: AlertsChange) => {
    setFailed('')
    const settle = polled.mutate((a) => withAlertsChange(a, c))
    try {
      await updateProjectAlerts(projectId, c)
    } catch (err) {
      setFailed(problem(err))
    } finally {
      settle() // loads what the server has, which also undoes a failed change
    }
  }

  const { data, error } = polled
  if (error instanceof ApiError && error.status === 404) return <NotFoundPage what="Project" />
  if (!data) return <section aria-busy={!error}>{error ? <LoadError /> : null}</section>
  if (!me) return <section aria-busy="true" />

  return (
    <section>
      <nav className={page.breadcrumb} aria-label="Breadcrumb">
        <Link to="/">All projects</Link>
      </nav>
      <h1 className={page.title}>{alertsHeadline(data)}</h1>
      <p className={page.lead}>{alertsLead(data)}</p>
      {!data.mailEnabled && <p className={styles.mailOff}>Email is off. Set MAIL_URL to send email.</p>}
      {!canChange && <p className={styles.mailOff}>Only admins can change alerts.</p>}
      {failed && (
        <p className={styles.failed} role="alert">
          {failed}
        </p>
      )}

      <section className={`${page.card} ${styles.card}`} aria-labelledby="kinds-title">
        <h2 id="kinds-title" className={`${page.cardTitle} ${styles.cardTitle}`}>
          Notifications
        </h2>
        <div className={page.scroll}>
          <table className={page.table}>
            <thead>
              <tr>
                <th scope="col">Alert</th>
                <th scope="col" className={styles.channel}>
                  Email
                </th>
                <th scope="col" className={styles.channel}>
                  Slack
                </th>
              </tr>
            </thead>
            <tbody>
              <KindRow kind="newIssue" name="New issue" alerts={data} canChange={canChange} onChange={change}>
                An error comes in for the first time.
              </KindRow>
              <KindRow kind="regression" name="Returning issue" alerts={data} canChange={canChange} onChange={change}>
                A resolved issue comes back and opens again.
              </KindRow>
              <KindRow kind="spike" name="Spike" alerts={data} canChange={canChange} onChange={change}>
                <ThresholdField
                  key={data.alerts.spike.threshold}
                  value={data.alerts.spike.threshold}
                  disabled={!canChange}
                  onChange={change}
                  onInvalid={setFailed}
                />
              </KindRow>
              <KindRow kind="digest" name="Summary" alerts={data} canChange={canChange} onChange={change}>
                <span className={styles.inline}>
                  Errors, new issues and the busiest issues of the past period, at 08:00:
                  <span className={form.selectBox}>
                    <select
                      className={`${form.select} ${styles.frequency}`}
                      aria-label="How often the summary comes"
                      disabled={!canChange}
                      value={data.alerts.digest.frequency}
                      onChange={(e) => change({ alerts: { digest: { frequency: e.target.value as DigestFrequency } } })}
                    >
                      {FREQUENCIES.map((f) => (
                        <option key={f.value} value={f.value}>
                          {f.label}
                        </option>
                      ))}
                    </select>
                    <ChevronDownIcon className={form.chevron} />
                  </span>
                </span>
                <span className={styles.when}>Weekly summaries come on Mondays, monthly ones on the 1st.</span>
              </KindRow>
            </tbody>
          </table>
        </div>
      </section>

      <section className={`${page.card} ${styles.card} ${styles.padded}`} aria-labelledby="slack-title">
        <h2 id="slack-title" className={page.cardTitle}>
          Slack
        </h2>
        <p className={form.note}>
          {canChange
            ? `Paste an incoming webhook from Slack (Apps, Incoming WebHooks), for the channel the alerts of ${data.project.name} should go to. It must start with https:// and stays secret: after saving, only its end is shown.`
            : `Slack messages about ${data.project.name} go to this webhook; only its end is shown.`}
        </p>
        <SlackWebhook
          saved={data.slackWebhook}
          readOnly={!canChange}
          onSave={async (url) => {
            await updateProjectAlerts(projectId, { slackWebhookUrl: url })
            polled.reload()
          }}
          onTest={async (url) => {
            await testProjectSlack(projectId, url)
          }}
        />
      </section>
    </section>
  )
}

interface KindRowProps {
  kind: keyof AlertKinds
  name: string
  alerts: ProjectAlerts
  /** canChange is false for members: the checkboxes show the choices but are disabled. */
  canChange: boolean
  onChange: (c: AlertsChange) => void
  /** children describe when the alert fires, with its setting. */
  children: ReactNode
}

/** KindRow is one kind of alert with its Email and Slack checkboxes. */
function KindRow({ kind, name, alerts, canChange, onChange, children }: KindRowProps) {
  const channels: AlertChannels = alerts.alerts[kind]
  const toggle = (channel: keyof AlertChannels, on: boolean) => onChange({ alerts: { [kind]: { [channel]: on } } })
  return (
    <tr>
      <td>
        <div className={styles.name}>{name}</div>
        <div className={styles.meta}>{children}</div>
      </td>
      <td className={styles.channel}>
        <input
          type="checkbox"
          className={styles.check}
          aria-label={`${name} by email`}
          title={alerts.mailEnabled ? undefined : 'Email is off. Set MAIL_URL to send email.'}
          disabled={!canChange || !alerts.mailEnabled}
          checked={channels.email}
          onChange={(e) => toggle('email', e.target.checked)}
        />
      </td>
      <td className={styles.channel}>
        <input
          type="checkbox"
          className={styles.check}
          aria-label={`${name} to Slack`}
          disabled={!canChange}
          checked={channels.slack}
          onChange={(e) => toggle('slack', e.target.checked)}
        />
      </td>
    </tr>
  )
}

/** ThresholdField is the spike threshold; it saves when it loses focus or on Enter. */
interface ThresholdFieldProps {
  value: number
  disabled: boolean
  onChange: (c: AlertsChange) => void
  onInvalid: (text: string) => void
}

function ThresholdField({ value, disabled, onChange, onInvalid }: ThresholdFieldProps) {
  const [text, setText] = useState(String(value))
  const save = () => {
    const threshold = parseThreshold(text)
    if (threshold === null) {
      onInvalid(`The spike threshold is a whole number from ${MIN_THRESHOLD} to ${MAX_THRESHOLD}.`)
      setText(String(value))
      return
    }
    if (threshold !== value) onChange({ alerts: { spike: { threshold } } })
  }
  return (
    <span className={styles.inline}>
      An hour with at least
      <input
        className={`${form.input} ${styles.threshold}`}
        type="number"
        inputMode="numeric"
        min={MIN_THRESHOLD}
        max={MAX_THRESHOLD}
        aria-label="Spike threshold, times the usual hourly errors"
        disabled={disabled}
        value={text}
        onChange={(e) => setText(e.target.value)}
        onBlur={save}
        onKeyDown={(e) => {
          if (e.key === 'Enter') save()
        }}
      />
      times the usual number of errors (the hourly average of the week before), and 10 or more.
    </span>
  )
}
