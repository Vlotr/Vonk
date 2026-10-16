import { renderToString } from 'react-dom/server'
import { MemoryRouter } from 'react-router'
import { expect, test } from 'vitest'
import { App } from './App'
import { TOOL_NAME } from './brand'

function render(path: string) {
  return renderToString(
    <MemoryRouter initialEntries={[path]}>
      <App />
    </MemoryRouter>,
  )
}

test('the shell shows the tool name from brand.ts', () => {
  expect(render('/')).toContain(TOOL_NAME)
})

test('an unknown URL shows the not found page inside the shell', () => {
  const html = render('/no/such/page')
  expect(html).toContain('Page not found')
  expect(html).toContain(TOOL_NAME)
})

test('the sign-in pages stand alone, outside the shell', () => {
  const login = render('/login')
  expect(login).toContain(`Sign in to ${TOOL_NAME}`)
  expect(login).not.toContain('All projects')
  expect(render('/setup')).toContain('Create the first account')
  expect(render('/forgot-password')).toContain('Reset your password')
})

test('the sidebar links to Settings', () => {
  expect(render('/')).toContain('href="/settings"')
})

test('the alerts, pages and status pages render inside the shell', () => {
  for (const path of ['/projects/1/alerts', '/projects/1/pages', '/settings/status']) {
    const html = render(path)
    expect(html).toContain(TOOL_NAME)
    expect(html).not.toContain('Page not found')
  }
})
