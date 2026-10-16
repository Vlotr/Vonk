import { type FormEvent, useState } from 'react'
import { problem } from '../lib/signin'
import button from './Button.module.css'
import form from './Form.module.css'
import styles from './SlackWebhook.module.css'

interface SlackWebhookProps {
  /** saved is the saved webhook, masked by the server; null when there is none. */
  saved: string | null
  /** readOnly shows only the masked webhook: for members, who cannot change alerts. */
  readOnly?: boolean
  /** onSave saves a webhook; "" removes it. */
  onSave: (url: string) => Promise<void>
  /** onTest sends a test message to url, or to the saved webhook without one. */
  onTest: (url?: string) => Promise<void>
}

/**
 * SlackWebhook shows the saved Slack webhook (only its end: the server
 * never sends the rest back), and a field to paste a new one, with "Send
 * test message" for both.
 */
export function SlackWebhook({ saved, readOnly = false, onSave, onTest }: SlackWebhookProps) {
  if (readOnly) {
    return saved === null ? (
      <p className={form.note}>No Slack webhook yet.</p>
    ) : (
      <div className={form.field}>
        <span className={form.label}>Webhook</span>
        <code className={styles.masked}>{saved}</code>
      </div>
    )
  }
  return <SlackWebhookForm saved={saved} onSave={onSave} onTest={onTest} />
}

function SlackWebhookForm({ saved, onSave, onTest }: Omit<SlackWebhookProps, 'readOnly'>) {
  const [editing, setEditing] = useState(false)
  const [url, setUrl] = useState('')
  const [busy, setBusy] = useState(false)
  const [notice, setNotice] = useState<{ ok: boolean; text: string }>()

  const run = async (action: () => Promise<void>, done: string) => {
    setBusy(true)
    setNotice(undefined)
    try {
      await action()
      setNotice({ ok: true, text: done })
      return true
    } catch (err) {
      setNotice({ ok: false, text: problem(err) })
      return false
    } finally {
      setBusy(false)
    }
  }

  const save = async (e: FormEvent) => {
    e.preventDefault()
    if (await run(() => onSave(url.trim()), 'The webhook is saved.')) {
      setEditing(false)
      setUrl('')
    }
  }
  const test = (typed?: string) => run(() => onTest(typed), 'Test message sent. Check the channel.')

  const showForm = editing || saved === null
  return (
    <div className={styles.webhook}>
      {saved !== null && !editing && (
        <div className={styles.saved}>
          <div className={form.field}>
            <span className={form.label}>Webhook</span>
            <code className={styles.masked}>{saved}</code>
          </div>
          <div className={form.buttons}>
            <button type="button" className={`${button.button} ${button.large}`} disabled={busy} onClick={() => test()}>
              Send test message
            </button>
            <button type="button" className={`${button.button} ${button.large}`} disabled={busy} onClick={() => setEditing(true)}>
              Change
            </button>
            <button type="button" className={`${button.button} ${button.large} ${button.danger}`} disabled={busy} onClick={() => run(() => onSave(''), 'The webhook is removed.')}>
              Remove
            </button>
          </div>
        </div>
      )}
      {showForm && (
        <form className={styles.form} onSubmit={save}>
          <label className={form.field}>
            <span className={form.label}>Webhook URL</span>
            <input
              className={form.input}
              type="url"
              placeholder="https://hooks.slack.com/services/…"
              autoComplete="off"
              spellCheck={false}
              required
              value={url}
              onChange={(e) => setUrl(e.target.value)}
            />
          </label>
          <div className={form.buttons}>
            <button type="submit" className={`${button.button} ${button.large} ${button.primary}`} disabled={busy}>
              Save webhook
            </button>
            <button type="button" className={`${button.button} ${button.large}`} disabled={busy || url.trim() === ''} onClick={() => test(url.trim())}>
              Send test message
            </button>
            {editing && (
              <button
                type="button"
                className={`${button.button} ${button.large}`}
                disabled={busy}
                onClick={() => {
                  setEditing(false)
                  setUrl('')
                  setNotice(undefined)
                }}
              >
                Cancel
              </button>
            )}
          </div>
        </form>
      )}
      {notice && (
        <p className={notice.ok ? form.success : form.error} role={notice.ok ? 'status' : 'alert'}>
          {notice.text}
        </p>
      )}
    </div>
  )
}
