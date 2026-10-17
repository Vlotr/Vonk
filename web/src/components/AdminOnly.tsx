import type { ReactNode } from 'react'
import { useMe } from '../layout/MeContext'
import { NotFoundPage } from '../pages/NotFoundPage'

/**
 * AdminOnly shows its children to admins. A member gets "Page not found",
 * as the server answers 404 for these routes anyway.
 */
export function AdminOnly({ children }: { children: ReactNode }) {
  const me = useMe()
  if (!me) return <section aria-busy="true" />
  if (me.role !== 'admin') return <NotFoundPage />
  return children
}
