import styles from './Logo.module.css'

// Logo is a three by three grid of squares: grey, one light orange in the
// middle and a dark orange corner, like the errors-per-hour bars.
export function Logo({ className }: { className?: string }) {
  const squares = []
  for (let row = 0; row < 3; row++) {
    for (let col = 0; col < 3; col++) {
      let squareClass = styles.square
      if (row === 1 && col === 1) squareClass = styles.middle
      if (row === 2 && col === 2) squareClass = styles.corner
      squares.push(<rect key={`${row}-${col}`} x={col * 7} y={row * 7} width={6} height={6} rx={1.5} className={squareClass} />)
    }
  }
  return (
    <svg className={`${styles.logo} ${className ?? ''}`} viewBox="0 0 20 20" aria-hidden="true">
      {squares}
    </svg>
  )
}
