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
