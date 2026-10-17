// A production build (npm run build) uploads its source maps to the
// monitoring tool when SENTRY_AUTH_TOKEN is set: a token from Settings →
// API tokens, SENTRY_URL the tool's BASE_URL, SENTRY_PROJECT the project's
// slug. Its errors then show src/main.ts instead of minified code. The
// development server (npm run dev) needs none of this.
import { sentryVitePlugin } from '@sentry/vite-plugin'
import { defineConfig } from 'vite'

export default defineConfig({
  // Maps are made for the upload, but the files do not point at them.
  build: { sourcemap: 'hidden' },
  plugins: process.env.SENTRY_AUTH_TOKEN
    ? [
        sentryVitePlugin({
          url: process.env.SENTRY_URL,
          org: 'monitor', // any word: the tool has no organizations
          project: process.env.SENTRY_PROJECT,
          authToken: process.env.SENTRY_AUTH_TOKEN,
          release: { name: process.env.VITE_SENTRY_RELEASE },
          // Uploaded maps are not deployed with the site.
          sourcemaps: { filesToDeleteAfterUpload: ['dist/**/*.map'] },
          telemetry: false,
        }),
      ]
    : [],
})
