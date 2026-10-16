// The shop's pages: a sign-in button in the header, product links that
// change the page with history.pushState (a page view each, as in a
// single-page app) and send product_viewed, and a payment that sends
// checkout_started and then fails on purpose.
import { configured, currentUser, signIn, signOut, track } from './analytics.js'

const status = document.querySelector('#status')
const account = document.querySelector('#account')
// Who "Sign in" signs in: user 42, or another id from ?user= (such as an email address).
const signInAs = new URLSearchParams(location.search).get('user') || '42'

function showUser() {
  const user = currentUser()
  account.textContent = user ? `Sign out (${user})` : `Sign in as ${signInAs === '42' ? 'user 42' : signInAs}`
  status.textContent = configured
    ? 'posthog-js and the Sentry SDK are set up.'
    : 'Set VITE_POSTHOG_TOKEN, VITE_POSTHOG_HOST and VITE_SENTRY_DSN before starting Vite (see README.md).'
}

account.addEventListener('click', () => {
  if (currentUser()) signOut()
  else signIn(signInAs)
  showUser()
})
showUser()

// Product pages inside the products page: the address changes, the page
// does not load again.
for (const link of document.querySelectorAll('[data-product]')) {
  link.addEventListener('click', (e) => {
    e.preventDefault()
    history.pushState({}, '', `/products/${link.dataset.product}`)
    document.querySelector('#product').textContent = `You are looking at ${link.textContent}.`
    track('product_viewed', { product: link.dataset.product })
  })
}

// The payment provider's answer has no price: a TypeError.
document.querySelector('#pay')?.addEventListener('click', () => {
  track('checkout_started', { total: 49.95, currency: 'EUR' })
  const answer = {}
  document.querySelector('#paid').textContent = answer.payment.price.toFixed(2)
})
