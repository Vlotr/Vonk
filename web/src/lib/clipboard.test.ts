// @vitest-environment happy-dom
import { afterEach, expect, test, vi } from 'vitest'
import { copyText } from './clipboard'

afterEach(() => {
  vi.restoreAllMocks()
})

test('copyText reports success when the clipboard API takes the text', async () => {
  const writeText = vi.fn().mockResolvedValue(undefined)
  vi.spyOn(navigator, 'clipboard', 'get').mockReturnValue({ writeText } as unknown as Clipboard)
  expect(await copyText('https://monitor.example.com/projects/1/issues/2')).toBe(true)
  expect(writeText).toHaveBeenCalledWith('https://monitor.example.com/projects/1/issues/2')
})

test('copyText falls back to a selected text field and reports what the browser says', async () => {
  vi.spyOn(navigator, 'clipboard', 'get').mockReturnValue({
    writeText: vi.fn().mockRejectedValue(new Error('not allowed')),
  } as unknown as Clipboard)
  const exec = vi.fn().mockReturnValue(true)
  document.execCommand = exec
  expect(await copyText('a')).toBe(true)
  expect(exec).toHaveBeenCalledWith('copy')

  exec.mockReturnValue(false)
  expect(await copyText('a')).toBe(false)
  expect(document.querySelector('textarea')).toBeNull() // the helper field is gone again
})
