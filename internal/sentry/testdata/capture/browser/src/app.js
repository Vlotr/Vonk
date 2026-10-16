import * as Sentry from '@sentry/browser';

Sentry.init({
  dsn: window.SENTRY_DSN,
  release: 'webshop@1.4.2',
  environment: 'production',
  sendDefaultPii: true,
});
Sentry.setUser({ id: '42', email: 'jan@example.com' });

function loadCart() {
  // The API forgot the total.
  return { items: 3 };
}

function renderCartTotal(cart) {
  return cart.total.toFixed(2);
}

function onCheckoutClick() {
  const cart = loadCart();
  document.querySelector('#total').textContent = renderCartTotal(cart);
}

document.querySelector('#checkout').addEventListener('click', onCheckoutClick);

fetch('/api/cart?session_token=sess_abc123&cvv=123')
  .then((response) => response.json())
  .then((cart) => {
    console.log('cart loaded', cart.items);
    history.pushState({}, '', '/checkout/payment?step=2&iban=NL91ABNA0417164300');
    document.body.dataset.ready = 'yes';
  });
