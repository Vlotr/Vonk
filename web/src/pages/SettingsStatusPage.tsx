import type { ReactNode } from 'react'
import { getToolStatus, setAdminSlack, testAdminSlack, type ToolStatus } from '../api/client'
import { AdminOnly } from '../components/AdminOnly'
import form from '../components/Form.module.css'
import { LoadError } from '../components/LoadError'
import { SettingsHeader } from '../components/SettingsHeader'
import { SlackWebhook } from '../components/SlackWebhook'
import { formatDateTime, timeAgo } from '../lib/format'
import { adminAlertLabel, diskFree, formatBytes, formatUptime, healthSentence, statusHeadline } from '../lib/status'
import { usePolling } from '../lib/usePolling'
import page from './Page.module.css'
import styles from './SettingsStatusPage.module.css'

// SettingsStatusPage shows how the tool itself is doing, for admins: the
// checks /health makes, the database, disk and queue, the last backup,
// the alerts admins got, and the Slack webhook for those alerts.
export function SettingsStatusPage() {
  return (
    <AdminOnly>
      <ToolStatusView />
    </AdminOnly>
  )
}

function ToolStatusView() {
  const polled = usePolling(getToolStatus, 'tool-status')
  const s = polled.data
  if (!s) return <section aria-busy={!polled.error}>{polled.error ? <LoadError /> : null}</section>

  const now = new Date()
  const disk = diskFree(s)
  return (
    <section>
      <SettingsHeader title={statusHeadline(s)} lead={`Version ${s.version}, running for ${formatUptime(s.uptimeSeconds)}. ${healthSentence(s)}`} />

      <section className={`${page.card} ${styles.stats}`} aria-label="Server">
        <dl className={styles.numbers}>
          <Stat label="Database" problem={s.db === 'error'}>
            {s.db === 'error' ? 'Does not answer' : formatBytes(s.databaseBytes)}
          </Stat>
          <Stat label="Disk free" problem={s.disk !== 'ok'} detail={disk.detail}>
            {disk.value}
          </Stat>
          <Stat label="Events in the last hour">{s.eventsLastHour.toLocaleString('en-US')}</Stat>
          <Stat label="Waiting to be stored" detail={`of at most ${s.queueCapacity.toLocaleString('en-US')}`}>
            {s.queueDepth.toLocaleString('en-US')}
          </Stat>
        </dl>
        {(s.disk === 'low' || s.disk === 'critical') && (
          <p className={styles.problem}>
            When the disk is full, errors can no longer be stored. Free up space or give the volume more room; a shorter "Keep data for" in
            Projects keeps less.
          </p>
        )}
      </section>

      <section className={`${page.card} ${styles.card}`} aria-labelledby="backup-title">
        <h2 id="backup-title" className={page.cardTitle}>
          Backups
        </h2>
        <BackupLine status={s} now={now} />
      </section>

      <section className={`${page.card} ${styles.card}`} aria-labelledby="admin-alerts-title">
        <h2 id="admin-alerts-title" className={page.cardTitle}>
          Alerts for admins
        </h2>
        <p className={form.note}>
          Admins hear about a disk that runs low (under 10 % or 1 GiB free), events the database could not store, and events turned away
          because the tool was too busy; each at most once in 12 hours.{' '}
          {s.mailEnabled ? `Mail goes to the ${s.admins === 1 ? 'admin' : `${s.admins} admins`}.` : 'Email is off. Set MAIL_URL to send email.'}
        </p>
        {s.adminAlerts.length === 0 ? (
          <p className={styles.none}>No alerts so far.</p>
        ) : (
          <ul className={styles.alerts}>
            {s.adminAlerts.map((a) => (
              <li key={a.kind}>
                <div className={styles.alertHead}>
                  <span className={styles.alertName}>{adminAlertLabel(a.kind)}</span>
                  <time dateTime={a.sentAt} title={new Date(a.sentAt).toLocaleString()}>
                    {timeAgo(new Date(a.sentAt), now)}
                  </time>
                </div>
                <div className={styles.alertText}>{a.message}</div>
              </li>
            ))}
          </ul>
        )}
        <h3 className={styles.subTitle}>Slack</h3>
        <p className={form.note}>Send the admin alerts to a Slack channel too, with an incoming webhook. It must start with https:// and stays secret: after saving, only its end is shown.</p>
        <SlackWebhook
          saved={s.adminSlackWebhook}
          onSave={async (url) => {
            await setAdminSlack(url)
            polled.reload()
          }}
          onTest={async (url) => {
            await testAdminSlack(url)
          }}
        />
      </section>
    </section>
  )
}

/** Stat is one number of the strip, with an optional line under it. */
function Stat({ label, problem, detail, children }: { label: string; problem?: boolean; detail?: string; children: ReactNode }) {
  return (
    <div className={styles.stat}>
      <dt>{label}</dt>
      <dd className={problem ? styles.problemValue : undefined}>{children}</dd>
      {detail && <dd className={styles.detail}>{detail}</dd>}
    </div>
  )
}

function BackupLine({ status, now }: { status: ToolStatus; now: Date }) {
  const b = status.lastBackup
  if (!b) {
    return <p className={styles.none}>No backup yet. One is made automatically before each database update, when the tool starts.</p>
  }
  return (
    <>
      <p className={styles.backup}>
        <code>{b.name}</code>, {formatBytes(b.sizeBytes)}, made{' '}
        <time dateTime={b.createdAt}>{formatDateTime(new Date(b.createdAt), now)}</time>.
      </p>
      <p className={form.note}>Made before each database update, in the backups folder of DATA_DIR; the five newest are kept.</p>
    </>
  )
}
