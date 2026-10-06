import type { AdminHealthResponse } from '@/types'

let cachedAdminHealth: AdminHealthResponse | null = null

export function getCachedAdminHealth(): AdminHealthResponse | null {
  return cachedAdminHealth
}

export function setCachedAdminHealth(response: AdminHealthResponse): void {
  cachedAdminHealth = response
}

export function clearCachedAdminHealth(): void {
  cachedAdminHealth = null
}
