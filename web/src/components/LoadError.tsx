import page from '../pages/Page.module.css'

/** LoadError is shown while a page has no data because the API did not answer. */
export function LoadError() {
  return <p className={page.lead}>The dashboard cannot reach the server right now. It tries again every minute.</p>
}
