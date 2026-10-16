// Sends browser errors to the monitoring tool with the official Sentry SDK.
// Open the page with the DSN of a project: /?dsn=http://<key>@localhost:8000/1
import * as Sentry from '@sentry/browser'

const status = document.querySelector('#status')
const dsn = new URLSearchParams(window.location.search).get('dsn')

if (dsn) {
  Sentry.init({ dsn })
  status.textContent = 'Sentry is set up. Click a button to send an error.'
} else {
  status.innerHTML = 'Add the DSN of your project to the address: <code>?dsn=http://&lt;key&gt;@localhost:8000/1</code>'
}

// A TypeError in a click handler: the product is missing.
document.querySelector('#type-error').addEventListener('click', () => {
  showPrice(undefined)
})

function showPrice(product) {
  document.querySelector('#price').textContent = product.price.toFixed(2)
}

// An unhandled rejection: nobody awaits loadStock or catches its error.
document.querySelector('#rejection').addEventListener('click', () => {
  loadStock()
})

async function loadStock() {
  throw new Error('Failed to fetch /api/stock')
}
