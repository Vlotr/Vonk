import { describe, expect, test } from 'vitest'
import type { Frame } from '../api/client'
import { codeLines, visibleFrames } from './frames'

function frame(file: string, inApp: boolean, extra: Partial<Frame> = {}): Frame {
  return { file, function: 'f', line: 10, inApp, preContext: [], contextLine: '', postContext: [], ...extra }
}

describe('codeLines', () => {
  test('numbers the lines around the error line and marks it', () => {
    const lines = codeLines(
      frame('/app/Http/Controllers/CheckoutController.php', true, {
        line: 88,
        preContext: ['a', 'b', 'c'],
        contextLine: 'error',
        postContext: ['d', 'e', 'f'],
      }),
    )
    expect(lines.map((l) => l.number)).toEqual([85, 86, 87, 88, 89, 90, 91])
    expect(lines.filter((l) => l.isError)).toEqual([{ number: 88, text: 'error', isError: true }])
  })

  test('a frame without source has no lines', () => {
    expect(codeLines(frame('app.js', true))).toEqual([])
  })
})

describe('visibleFrames', () => {
  const frames = [frame('a', true), frame('v1', false), frame('v2', false), frame('b', true), frame('v3', false)]

  test('hides framework frames and counts them', () => {
    const { shown, hidden } = visibleFrames(frames, false)
    expect(shown.map((f) => f.file)).toEqual(['a', 'b'])
    expect(hidden).toBe(3)
  })

  test('shows every frame, in order, when asked', () => {
    const { shown, hidden } = visibleFrames(frames, true)
    expect(shown.map((f) => f.file)).toEqual(['a', 'v1', 'v2', 'b', 'v3'])
    expect(hidden).toBe(0)
  })

  test("shows all frames when none is the app's own", () => {
    const vendor = [frame('v1', false), frame('v2', false)]
    expect(visibleFrames(vendor, false)).toEqual({ shown: vendor, hidden: 0 })
  })
})
