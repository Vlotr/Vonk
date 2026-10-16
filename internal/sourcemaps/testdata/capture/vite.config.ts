// The fixtures' build; VITE_BANNER=1 makes the second one. With
// SENTRY_AUTH_TOKEN set, @sentry/vite-plugin uploads the source maps to
// SENTRY_URL, which must be the address in the token (the monitor's
// BASE_URL; for the fixtures, tools/capture -forward in front of it).
import { sentryVitePlugin } from '@sentry/vite-plugin'
import { defineConfig } from 'vite'

export default defineConfig({
  build: { sourcemap: 'hidden', outDir: process.env.OUT_DIR ?? 'dist' },
  plugins: process.env.SENTRY_AUTH_TOKEN
    ? [
        sentryVitePlugin({
          url: process.env.SENTRY_URL,
          org: process.env.SENTRY_ORG,
          authToken: process.env.SENTRY_AUTH_TOKEN,
          project: process.env.SENTRY_PROJECT,
          release: { name: process.env.SENTRY_RELEASE },
          telemetry: false,
        }),
      ]
    : [],
})
