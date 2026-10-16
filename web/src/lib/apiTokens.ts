// Text for Settings → API tokens. Pure functions, tested in apiTokens.test.ts.

import { timeAgo } from './format'

/** tokenHint shows a token by its last four characters, as the server keeps them: "sntrys_…pHSI". */
export function tokenHint(hint: string): string {
  return `sntrys_…${hint}`
}

/** lastUsedText says when a token was last used for an upload: "4 min ago", or "Never". */
export function lastUsedText(lastUsedAt: string | null, now: Date): string {
  return lastUsedAt ? timeAgo(new Date(lastUsedAt), now) : 'Never'
}

/**
 * viteSnippet sets up @sentry/vite-plugin for this tool (11.8 was tested):
 * source maps built hidden (no sourceMappingURL in the files), uploaded to
 * baseURL for the project, and deleted from the build afterwards, so they
 * are never served. The new Sentry CLI inside the plugin needs url and org
 * spelled out; the org can be any word here.
 */
export function viteSnippet(baseURL: string, projectSlug: string): string {
  return `import { sentryVitePlugin } from '@sentry/vite-plugin'
import { defineConfig } from 'vite'

export default defineConfig({
  build: { sourcemap: 'hidden' },
  plugins: [
    sentryVitePlugin({
      url: '${baseURL}',
      org: 'monitor',
      project: '${projectSlug}',
      authToken: process.env.SENTRY_AUTH_TOKEN,
      sourcemaps: { filesToDeleteAfterUpload: ['./dist/**/*.map'] },
      telemetry: false,
    }),
  ],
})`
}

/** buildEnv is how the build gets the token, with the Sentry CLI's own error reports turned off. */
export function buildEnv(token: string): string {
  return `SENTRY_AUTH_TOKEN=${token}
SENTRY_CLI_NO_TELEMETRY=1`
}

/**
 * cliCommands uploads a build with sentry-cli (3.8 was tested): first the
 * debug ids that tie each file to its map, then the upload. The token
 * carries this tool's address, so sentry-cli needs no --url or --org.
 */
export function cliCommands(projectSlug: string): string {
  return `npx @sentry/cli sourcemaps inject ./dist
npx @sentry/cli sourcemaps upload --project ${projectSlug} ./dist`
}
