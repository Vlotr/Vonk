import { defineConfig } from '@playwright/test'

// End-to-end tests: the real binary, the real SDKs (sentry-laravel in the
// Laravel example, @sentry/browser in the browser example) and the dashboard
// in Chromium. global-setup.ts starts everything; `make e2e` runs it.
export default defineConfig({
  testDir: '.',
  testMatch: '*.spec.ts',
  globalSetup: './global-setup.ts',
  timeout: 90_000,
  workers: 1,
  reporter: [['list']],
  outputDir: 'test-results',
  use: {
    browserName: 'chromium',
    // The designs are drawn at 1360 px wide and twice the pixel density, so
    // the screenshots can be compared with them side by side.
    viewport: { width: 1360, height: 960 },
    deviceScaleFactor: 2,
    trace: 'retain-on-failure',
  },
})
