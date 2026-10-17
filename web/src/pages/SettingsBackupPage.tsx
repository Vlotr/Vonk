import { type FormEvent, useRef, useState } from 'react'
import { BACKUP_DOWNLOAD_URL, type BackupInfo, cancelRestore, getBackupInfo, uploadRestore } from '../api/client'
import { AdminOnly } from '../components/AdminOnly'
import button from '../components/Button.module.css'
import { CopyBlock } from '../components/CopyBlock'
import form from '../components/Form.module.css'
import { LoadError } from '../components/LoadError'
import { SettingsHeader } from '../components/SettingsHeader'
import { backupKindLabel, PG_DUMP, PG_RESTORE, restoreCommand, uploadProblem } from '../lib/backup'
import { formatDateTime } from '../lib/format'
import { problem } from '../lib/signin'
import { formatBytes } from '../lib/status'
import { usePolling } from '../lib/usePolling'
import page from './Page.module.css'
import styles from './SettingsBackupPage.module.css'

// SettingsBackupPage is where admins download a copy of the whole database,
// see the automatic backups, and restore a backup. A restore never swaps
// the database while the tool runs: the upload is checked and waits for
// the next start ("Restart required"). On Postgres the page explains
// pg_dump and pg_restore instead. There is no design for this page; it
// follows Settings → Status.
export function SettingsBackupPage() {
  return (
    <AdminOnly>
      <BackupView />
    </AdminOnly>
  )
}

function BackupView() {
  const polled = usePolling(getBackupInfo, 'backup')
  const info = polled.data
  if (!info) return <section aria-busy={!polled.error}>{polled.error ? <LoadError /> : null}</section>

  return (
    <section>
      <SettingsHeader
        title="Backup"
        lead="A backup is a copy of the whole database: projects, issues and events, page statistics, users and settings. Download one to keep somewhere else, or restore one."
      />
      {info.databaseKind === 'postgres' ? (
        <PostgresCard />
      ) : (
        <>
          <DownloadCard info={info} />
          <RestoreCard info={info} onChange={polled.reload} />
          <AutomaticCard info={info} />
        </>
      )}
    </section>
  )
}

function PostgresCard() {
  return (
    <section className={`${page.card} ${styles.card}`} aria-labelledby="postgres-title">
      <h2 id="postgres-title" className={page.cardTitle}>
        Postgres
      </h2>
      <p className={styles.text}>
        The database is Postgres, so the tool makes no backups of it and restores none. Back it up with pg_dump, also before each update:
      </p>
      <CopyBlock label="Backup command" value={PG_DUMP} />
      <p className={styles.text}>And put a backup back with pg_restore, while the tool is stopped:</p>
      <CopyBlock label="Restore command" value={PG_RESTORE} />
      <p className={form.note}>Your database host may make backups too.</p>
    </section>
  )
}

function DownloadCard({ info }: { info: BackupInfo }) {
  return (
    <section className={`${page.card} ${styles.card}`} aria-labelledby="download-title">
      <h2 id="download-title" className={page.cardTitle}>
        Download a backup
      </h2>
      <p className={styles.text}>
        The file is a SQLite database of about {formatBytes(info.databaseBytes)}, made while the tool keeps running. Uploaded source maps are not in
        it: upload them again after a restore.
      </p>
      {info.canDownload ? (
        <div className={form.buttons}>
          <a className={`${button.button} ${button.large} ${button.primary}`} href={BACKUP_DOWNLOAD_URL} download>
            Download backup
          </a>
        </div>
      ) : (
        <p className={form.error} role="alert">
          There is not enough free disk space for the copy a download makes ({formatBytes(info.diskFreeBytes)} free). Free up disk space first.
        </p>
      )}
    </section>
  )
}

function RestoreCard({ info, onChange }: { info: BackupInfo; onChange: () => void }) {
  const fileInput = useRef<HTMLInputElement>(null)
  const [file, setFile] = useState<File>()
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  const upload = async (e: FormEvent) => {
    e.preventDefault()
    if (!file) return
    const tooBig = uploadProblem(file, info.maxUploadBytes)
    if (tooBig) {
      setError(tooBig)
      return
    }
    setBusy(true)
    setError('')
    try {
      await uploadRestore(file)
      setFile(undefined)
      if (fileInput.current) fileInput.current.value = ''
      onChange()
    } catch (err) {
      setError(problem(err))
    } finally {
      setBusy(false)
    }
  }

  const cancel = async () => {
    setError('')
    try {
      await cancelRestore()
      onChange()
    } catch (err) {
      setError(problem(err))
    }
  }

  const now = new Date()
  return (
    <section className={`${page.card} ${styles.card}`} aria-labelledby="restore-title">
      <h2 id="restore-title" className={page.cardTitle}>
        Restore a backup
      </h2>
      {info.restorePending && (
        <div className={styles.notice} role="status">
          <h3 className={styles.noticeTitle}>Restart required</h3>
          <p className={styles.text}>
            The backup ({formatBytes(info.restorePending.sizeBytes)}, uploaded{' '}
            <time dateTime={info.restorePending.stagedAt}>{formatDateTime(new Date(info.restorePending.stagedAt), now)}</time>) is checked and waits
            for the next start. Restart the tool to restore it: at the start it moves the current database to the backups folder and puts this
            backup in its place. What comes in until then is not in the restored database. After the restart, sign in with an account of the
            backup.
          </p>
          <div className={form.buttons}>
            <button type="button" className={button.button} onClick={cancel}>
              Cancel the restore
            </button>
          </div>
        </div>
      )}
      {info.restoreRejected && (
        <div className={styles.notice} role="status">
          <h3 className={styles.noticeTitle}>The last restore was refused</h3>
          <p className={styles.text}>
            At the start on <time dateTime={info.restoreRejected.rejectedAt}>{formatDateTime(new Date(info.restoreRejected.rejectedAt), now)}</time>{' '}
            the backup did not pass the check again, so the tool kept its database; the server log says why. The file is kept as{' '}
            <code>{info.restoreRejected.name}</code> in DATA_DIR.
          </p>
          <div className={form.buttons}>
            <button type="button" className={button.button} onClick={cancel}>
              Remove the file
            </button>
          </div>
        </div>
      )}
      <p className={styles.text}>
        Upload a backup this tool made (the download above, or an automatic one). It is checked first: a damaged file, or one from a newer version
        of the tool, is refused and nothing changes. The restore itself happens when the tool restarts.
      </p>
      <form className={styles.upload} onSubmit={upload}>
        <label className={form.field}>
          <span className={form.label}>Backup file</span>
          <input
            ref={fileInput}
            className={styles.file}
            type="file"
            accept=".db,.sqlite,.sqlite3,application/vnd.sqlite3,application/x-sqlite3"
            onChange={(e) => {
              setFile(e.target.files?.[0])
              setError('')
            }}
          />
        </label>
        <p className={form.note}>At most {formatBytes(info.maxUploadBytes)} fits now.</p>
        {error && (
          <p className={form.error} role="alert">
            {error}
          </p>
        )}
        <div className={form.buttons}>
          <button type="submit" className={`${button.button} ${button.large} ${button.primary}`} disabled={!file || busy}>
            {busy ? 'Uploading and checking…' : 'Upload and check'}
          </button>
        </div>
      </form>
    </section>
  )
}

function AutomaticCard({ info }: { info: BackupInfo }) {
  const newest = info.backups[0]
  return (
    <section className={`${page.card} ${styles.list}`} aria-labelledby="automatic-title">
      <h2 id="automatic-title" className={`${page.cardTitle} ${styles.listTitle}`}>
        Automatic backups
      </h2>
      <p className={`${styles.text} ${styles.listIntro}`}>
        Made when the tool starts, before each database update and before a restore, in <code>{info.backupsDir}</code>. The five newest of each kind
        are kept.
      </p>
      {info.backups.length === 0 ? (
        <p className={`${page.empty} ${styles.empty}`}>No automatic backups yet.</p>
      ) : (
        <>
          <div className={page.scroll}>
            <table className={page.table}>
              <thead>
                <tr>
                  <th scope="col">Backup</th>
                  <th scope="col">Made</th>
                  <th scope="col" className={page.fit}>
                    Size
                  </th>
                </tr>
              </thead>
              <tbody>
                {info.backups.map((b) => (
                  <tr key={b.name}>
                    <td>
                      <div className={styles.name}>{backupKindLabel(b.kind, b.version)}</div>
                      <code className={styles.meta}>{b.name}</code>
                    </td>
                    <td className={styles.meta}>
                      <time dateTime={b.createdAt}>{formatDateTime(new Date(b.createdAt), new Date())}</time>
                    </td>
                    <td className={`${page.fit} ${styles.meta}`}>{formatBytes(b.sizeBytes)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          <div className={styles.listText}>
            <p className={form.note}>
              To go back to one of them, run <code>monitor restore</code> with its path on the server (in Docker:{' '}
              <code>docker exec &lt;container&gt; /monitor restore …</code>) and restart the tool. For the newest:
            </p>
            <CopyBlock label="Restore command" value={restoreCommand(info.backupsDir, newest.name)} />
          </div>
        </>
      )}
    </section>
  )
}
