import { apiClient } from './client'

export interface MarketplaceBalance {
  eligible_available: string
  reserved: string
  excluded_available: string
  debt: string
  migrated: boolean
  reconciled: boolean
  currency: string
}
export interface CatalogTier { from_tokens: number; input_per_million: string; output_per_million: string; cache_read_per_million: string; cache_write_per_million: string }
export interface CatalogModel { public_model: string; actual_model: string; context_limit: number; max_output_tokens: number; rate_multiplier: string; tiers: CatalogTier[] }
export interface CatalogGroup { group_id: number; name: string; platform: string; protocol: string; models: CatalogModel[] }
export interface MarketplaceCatalog { version: string | number; groups: CatalogGroup[]; currency: string; funding: unknown }
export async function getMarketplaceBalance(signal?: AbortSignal): Promise<MarketplaceBalance> {
  const { data } = await apiClient.get<MarketplaceBalance>('/marketplace/balance', { signal })
  return data
}
export async function getMarketplaceCatalog(signal?: AbortSignal): Promise<MarketplaceCatalog> {
  const { data } = await apiClient.get<MarketplaceCatalog>('/marketplace/catalog', { signal })
  return data
}
