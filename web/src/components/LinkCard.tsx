import button from './Button.module.css'
import { CopyBlock } from './CopyBlock'
import form from './Form.module.css'
import styles from './LinkCard.module.css'

/** A link just made, shown once: the server keeps only a hash of its token. */
export interface ShownLink {
  title: string
  link: string
  text: string
}

/** LinkCard shows a link that was just made, with a Copy button. */
export function LinkCard({ shown, onDone }: { shown: ShownLink; onDone: () => void }) {
  return (
    <section className={`${form.card} ${styles.card}`} aria-labelledby="link-title">
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
