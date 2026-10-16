import { showBanner } from './banner'
import { onCheckoutClick, onFreeClick } from './checkout'

// The fixtures' second build (VITE_BANNER=1) shows a banner: its code comes
// before the checkout's, so the minified names and positions differ.
if (import.meta.env.VITE_BANNER) showBanner()
document.querySelector('#type-error')?.addEventListener('click', () => onCheckoutClick())
document.querySelector('#free')?.addEventListener('click', () => onFreeClick())
