// The shop's pages: a sign-in button in the header, product links that
// change the page with history.pushState (a page view each, as in a
// single-page app), and a payment that fails on purpose.
import { configured, currentUser, signIn, signOut } from './analytics.js'

const status = document.querySelector('#status')
const account = document.querySelector('#account')

function showUser() {
  const user = currentUser()
  account.textContent = user ? `Sign out (user ${user})` : 'Sign in as user 42'
  status.textContent = configured
    ? 'posthog-js and the Sentry SDK are set up.'
    : 'Set VITE_POSTHOG_TOKEN, VITE_POSTHOG_HOST and VITE_SENTRY_DSN before starting Vite (see README.md).'
}

account.addEventListener('click', () => {
  if (currentUser()) signOut()
  else signIn(42)
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
  })
}

// The payment provider's answer has no price: a TypeError.
document.querySelector('#pay')?.addEventListener('click', () => {
  const answer = {}
  document.querySelector('#paid').textContent = answer.payment.price.toFixed(2)
})
