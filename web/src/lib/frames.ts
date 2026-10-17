import type { Frame } from '../api/client'

export interface CodeLine {
  number: number
  text: string
  isError: boolean
}

/** codeLines numbers a frame's source: the lines before, the error line, the lines after. */
export function codeLines(frame: Frame): CodeLine[] {
  if (frame.contextLine === '' && frame.preContext.length === 0 && frame.postContext.length === 0) return []
  const first = frame.line - frame.preContext.length
  const texts = [...frame.preContext, frame.contextLine, ...frame.postContext]
  return texts.map((text, i) => ({ number: first + i, text, isError: i === frame.preContext.length }))
}

/**
 * visibleFrames hides framework frames (not in-app) unless showFramework is
 * set, and counts what it hid. When no frame is the app's own, all are shown:
 * otherwise the trace would be empty.
 */
export function visibleFrames(frames: Frame[], showFramework: boolean): { shown: Frame[]; hidden: number } {
  const own = frames.filter((f) => f.inApp)
  if (showFramework || own.length === 0) return { shown: frames, hidden: 0 }
  return { shown: own, hidden: frames.length - own.length }
}

/**
 * frameKey names a frame of frames by file, line and function, plus how many
 * frames before it have those three too (recursion repeats them). A newer
 * event of the same issue has new frame objects but the same keys, so the
 * viewer's open and closed frames still apply after a poll.
 */
export function frameKey(frame: Frame, frames: Frame[]): string {
  const name = `${frame.file}:${frame.line}:${frame.function}`
  const before = frames
    .slice(0, Math.max(0, frames.indexOf(frame)))
    .filter((f) => f.file === frame.file && f.line === frame.line && f.function === frame.function).length
  return `${name}#${before}`
}

/**
 * isFrameOpen tells whether a frame shows its source: as the viewer chose
 * (choices, by frameKey), or else open for the first in-app frame with source.
 */
export function isFrameOpen(frame: Frame, frames: Frame[], choices: Map<string, boolean>): boolean {
  const chosen = choices.get(frameKey(frame, frames))
  if (chosen !== undefined) return chosen
  return frames.find((f) => f.inApp && codeLines(f).length > 0) === frame
}
