import page from '../pages/Page.module.css'

/** LoadError is shown while a page has no data because the API did not answer. */
export function LoadError() {
  return <p className={page.lead}>The dashboard cannot reach the server right now. It tries again every minute.</p>
}

/** OlderData is the line above data that is still on screen after a load failed (new filters, or a poll). */
export function OlderData({ className }: { className?: string }) {
  return (
    <p className={className} role="status">
      Could not load; showing older results.
    </p>
  )
}
