import { resolve } from 'node:path'
import { defineConfig } from 'vite'

// Three real pages (each loads anew, so posthog-js sends a page leave for
// the one before); /products/<name> exists only through history.pushState.
export default defineConfig({
  appType: 'mpa',
  build: {
    rollupOptions: {
      input: {
        home: resolve(import.meta.dirname, 'index.html'),
        products: resolve(import.meta.dirname, 'products/index.html'),
        checkout: resolve(import.meta.dirname, 'checkout/index.html'),
      },
    },
  },
})
