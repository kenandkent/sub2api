import { apiClient } from './client'

export interface MarketplaceBalance {
  eligible_available: string
  reserved: string
  excluded: string
  currency: string
}
export async function getMarketplaceBalance(signal?: AbortSignal): Promise<MarketplaceBalance> {
  const { data } = await apiClient.get<MarketplaceBalance>('/marketplace/balance', { signal })
  return data
}
