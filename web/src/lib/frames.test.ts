import { describe, expect, test } from 'vitest'
import type { Frame } from '../api/client'
import { codeLines, frameKey, isFrameOpen, visibleFrames } from './frames'

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

describe('isFrameOpen', () => {
  const withCode = { preContext: ['a'], contextLine: 'b', postContext: ['c'] }
  const event1 = [frame('/app/Http/Controllers/CheckoutController.php', true, { line: 14, ...withCode }), frame('/vendor/x.php', false, { line: 3, ...withCode }), frame('/public/index.php', true, { line: 20, ...withCode })]

  test('the first in-app frame with source is open, the rest closed', () => {
    const choices = new Map<string, boolean>()
    expect(event1.map((f) => isFrameOpen(f, event1, choices))).toEqual([true, false, false])
  })

  test("the viewer's choices carry over to a newer event with the same frames", () => {
    const choices = new Map([
      [frameKey(event1[0]), false],
      [frameKey(event1[2]), true],
    ])
    // A newer event: new objects, the same files and lines, one frame more.
    const event2 = [frame('/app/Http/Controllers/CheckoutController.php', true, { line: 14, ...withCode }), frame('/vendor/y.php', false, { line: 9, ...withCode }), frame('/public/index.php', true, { line: 20, ...withCode })]
    expect(event2.map((f) => isFrameOpen(f, event2, choices))).toEqual([false, false, true])
  })

  test('a frame is known by file and line', () => {
    expect(frameKey(frame('/app/A.php', true, { line: 7 }))).toBe('/app/A.php:7')
  })
})
