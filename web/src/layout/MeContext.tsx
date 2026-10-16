import { createContext, useContext } from 'react'
import type { Me } from '../api/client'

/**
 * MeContext shares the signed-in user the shell loads. It is undefined
 * until GET /ui/api/me answers; pages that differ for admins wait for it.
 */
export const MeContext = createContext<Me | undefined>(undefined)

export function useMe(): Me | undefined {
  return useContext(MeContext)
}
