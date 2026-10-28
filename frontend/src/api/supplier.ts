import { apiClient } from './client'
import type { UserCapabilities } from '@/types/marketplace'

export async function getCapabilities(signal?: AbortSignal): Promise<UserCapabilities> {
  const { data } = await apiClient.get<UserCapabilities>('/user/capabilities', { signal })
  return data
}
