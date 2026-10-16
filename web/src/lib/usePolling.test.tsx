// @vitest-environment happy-dom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, expect, test } from 'vitest'
import { type Polled, usePolling } from './usePolling'

// React wants to know it runs under act().
;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

/** A load whose answers the test gives one by one. */
function controlledLoad() {
  const calls: { resolve: (v: string) => void; reject: (e: unknown) => void }[] = []
  const load = () =>
    new Promise<string>((resolve, reject) => {
      calls.push({ resolve, reject })
    })
  return { load, calls }
}

let root: Root
let container: HTMLElement
let latest: Polled<string>

function Probe({ load, refreshKey }: { load: () => Promise<string>; refreshKey: string }) {
  latest = usePolling(load, 'thing', refreshKey)
  return null
}

beforeEach(() => {
  container = document.createElement('div')
  root = createRoot(container)
})

afterEach(() => {
  act(() => root.unmount())
})

async function settle() {
  // Let resolved promises run their then().
  await act(async () => {
    await Promise.resolve()
  })
}

test('new filters keep the old data, marked stale, until the new data arrives', async () => {
  const { load, calls } = controlledLoad()
  act(() => root.render(<Probe load={load} refreshKey="a" />))
  calls[0].resolve('rows for a')
  await settle()
  expect(latest.data).toBe('rows for a')
  expect(latest.stale).toBe(false)

  act(() => root.render(<Probe load={load} refreshKey="b" />))
  expect(latest.data).toBe('rows for a')
  expect(latest.stale).toBe(true)

  calls[1].resolve('rows for b')
  await settle()
  expect(latest.data).toBe('rows for b')
  expect(latest.stale).toBe(false)
  expect(latest.error).toBeUndefined()
})

test('a failed load under new filters keeps the old data, ends stale and sets error', async () => {
  const { load, calls } = controlledLoad()
  act(() => root.render(<Probe load={load} refreshKey="a" />))
  calls[0].resolve('rows for a')
  await settle()

  act(() => root.render(<Probe load={load} refreshKey="b" />))
  calls[1].reject(new Error('offline'))
  await settle()
  expect(latest.data).toBe('rows for a')
  expect(latest.stale).toBe(false)
  expect(latest.error).toBeInstanceOf(Error)

  // The next load that works clears the error.
  act(() => latest.reload())
  calls[2].resolve('rows for b')
  await settle()
  expect(latest.data).toBe('rows for b')
  expect(latest.error).toBeUndefined()
})

test('mutate changes the data at once and holds loads back until it settles', async () => {
  const { load, calls } = controlledLoad()
  act(() => root.render(<Probe load={load} refreshKey="a" />))
  calls[0].resolve('3 open')
  await settle()

  let done = () => {}
  act(() => {
    done = latest.mutate(() => '2 open')
  })
  expect(latest.data).toBe('2 open')

  // A reload (or the minute poll) while the change is on its way does nothing.
  act(() => latest.reload())
  expect(calls).toHaveLength(1)

  act(() => done())
  expect(calls).toHaveLength(2)
  // Settling twice loads once.
  act(() => done())
  expect(calls).toHaveLength(2)

  calls[1].resolve('2 open, confirmed')
  await settle()
  expect(latest.data).toBe('2 open, confirmed')
})

test('a change that settles after the page is gone starts no load', async () => {
  const { load, calls } = controlledLoad()
  act(() => root.render(<Probe load={load} refreshKey="a" />))
  calls[0].resolve('rows')
  await settle()

  let done = () => {}
  act(() => {
    done = latest.mutate((d) => d)
  })
  act(() => root.unmount())
  done()
  expect(calls).toHaveLength(1)

  // afterEach unmounts again; give it a fresh root.
  root = createRoot(container)
})
