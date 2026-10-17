import { renderToString } from 'react-dom/server'
import { expect, test } from 'vitest'
import { SlackWebhook } from './SlackWebhook'

const noop = async () => {}

test('admins get the field and the buttons', () => {
  const html = renderToString(<SlackWebhook saved={null} onSave={noop} onTest={noop} />)
  expect(html).toContain('Webhook URL')
  expect(html).toContain('Send test message')
})

test('read only shows the masked webhook and nothing to change', () => {
  const html = renderToString(<SlackWebhook saved="https://hooks.slack.com/…/abcd" readOnly onSave={noop} onTest={noop} />)
  expect(html).toContain('https://hooks.slack.com/…/abcd')
  expect(html).not.toContain('<button')
  expect(html).not.toContain('<input')

  const none = renderToString(<SlackWebhook saved={null} readOnly onSave={noop} onTest={noop} />)
  expect(none).toContain('No Slack webhook yet.')
  expect(none).not.toContain('<input')
})
