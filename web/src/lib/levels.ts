// Bars for events per hour come in five levels, the colors --color-heat-0 to
// --color-heat-4: none, then four quarters of the busiest hour (of the row,
// or of the whole table on the All projects page).

export type Level = 0 | 1 | 2 | 3 | 4

/** barLevel maps an hour's count to a level relative to the row maximum. */
export function barLevel(count: number, max: number): Level {
  if (count <= 0 || max <= 0) return 0
  return Math.min(4, Math.max(1, Math.ceil((count / max) * 4))) as Level
}

/**
 * barLevels maps a whole row of hourly counts, relative to the row's busiest
 * hour or to shared, the busiest hour of a whole table (rows that share it
 * can be compared: a quiet project stays light next to a busy one).
 */
export function barLevels(counts: number[], shared = 0): Level[] {
  const max = Math.max(shared, ...counts)
  return counts.map((n) => barLevel(n, max))
}
