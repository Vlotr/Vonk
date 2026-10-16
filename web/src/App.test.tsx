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
