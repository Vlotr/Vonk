import { expect, test } from 'vitest'
import { buildEnv, cliCommands, lastUsedText, tokenHint, viteSnippet } from './apiTokens'

test('a token shows by its last four characters, and when it was used', () => {
  const now = new Date('2026-10-16T12:00:00Z')
  expect(tokenHint('pHSI')).toBe('sntrys_…pHSI')
  expect(lastUsedText(null, now)).toBe('Never')
  expect(lastUsedText('2026-10-16T11:56:00Z', now)).toBe('4 min ago')
})

test('the setup names this server and the project by its slug', () => {
  const vite = viteSnippet('https://monitor.example.com', 'webshop')
  expect(vite).toContain("url: 'https://monitor.example.com'")
  expect(vite).toContain("project: 'webshop'")
  expect(vite).toContain("sourcemap: 'hidden'")
  expect(vite).toContain('authToken: process.env.SENTRY_AUTH_TOKEN')
  expect(vite).toContain('telemetry: false')
  expect(buildEnv('sntrys_abc')).toBe('SENTRY_AUTH_TOKEN=sntrys_abc\nSENTRY_CLI_NO_TELEMETRY=1')
  expect(cliCommands('webshop')).toBe(
    'npx @sentry/cli sourcemaps inject ./dist\nnpx @sentry/cli sourcemaps upload --project webshop ./dist',
  )
})
