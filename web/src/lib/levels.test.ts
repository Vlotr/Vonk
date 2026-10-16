import { describe, expect, test } from 'vitest'
import { barLevel, barLevels } from './levels'

describe('barLevel', () => {
  test('zero events is level 0, the row maximum level 4', () => {
    expect(barLevel(0, 10)).toBe(0)
    expect(barLevel(10, 10)).toBe(4)
  })

  test('steps by quarters of the row maximum', () => {
    expect(barLevel(1, 100)).toBe(1)
    expect(barLevel(25, 100)).toBe(1)
    expect(barLevel(26, 100)).toBe(2)
    expect(barLevel(50, 100)).toBe(2)
    expect(barLevel(51, 100)).toBe(3)
    expect(barLevel(75, 100)).toBe(3)
    expect(barLevel(76, 100)).toBe(4)
  })

  test('any event shows, however small next to the maximum', () => {
    expect(barLevel(1, 1000)).toBe(1)
  })

  test('without events every bar is level 0', () => {
    expect(barLevel(0, 0)).toBe(0)
  })
})

describe('barLevels', () => {
  test('maps a row relative to its own maximum', () => {
    expect(barLevels([0, 1, 2, 3, 4, 8])).toEqual([0, 1, 1, 2, 2, 4])
    expect(barLevels([0, 0, 0])).toEqual([0, 0, 0])
  })
})
