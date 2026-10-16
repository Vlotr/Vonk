// Bars for events per hour come in five levels, the colors --color-heat-0 to
// --color-heat-4: none, then four quarters of the row's busiest hour.

export type Level = 0 | 1 | 2 | 3 | 4

/** barLevel maps an hour's count to a level relative to the row maximum. */
export function barLevel(count: number, max: number): Level {
  if (count <= 0 || max <= 0) return 0
  return Math.min(4, Math.max(1, Math.ceil((count / max) * 4))) as Level
}

/** barLevels maps a whole row of hourly counts. */
export function barLevels(counts: number[]): Level[] {
  const max = Math.max(0, ...counts)
  return counts.map((n) => barLevel(n, max))
}
