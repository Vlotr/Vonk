// Sends browser errors to the monitoring tool with the official Sentry SDK.
// Open the page with the DSN of a project: /?dsn=http://<key>@localhost:8000/1
import * as Sentry from '@sentry/browser'

interface Product {
  name: string
  price: number
}

const status = document.querySelector<HTMLElement>('#status')!
const dsn = new URLSearchParams(window.location.search).get('dsn')

if (dsn) {
  Sentry.init({ dsn, release: import.meta.env.VITE_SENTRY_RELEASE })
  status.textContent = 'Sentry is set up. Click a button to send an error.'
} else {
  status.innerHTML = 'Add the DSN of your project to the address: <code>?dsn=http://&lt;key&gt;@localhost:8000/1</code>'
}

// A TypeError in a click handler: the product is missing.
document.querySelector('#type-error')!.addEventListener('click', () => {
  showPrice(undefined)
})

function showPrice(product: Product | undefined): void {
  const price = document.querySelector<HTMLElement>('#price')!
  price.textContent = (product as Product).price.toFixed(2)
}

// An unhandled rejection: nobody awaits loadStock or catches its error.
document.querySelector('#rejection')!.addEventListener('click', () => {
  loadStock()
})

async function loadStock(): Promise<number> {
  throw new Error('Failed to fetch /api/stock')
}
