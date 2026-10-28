export type WorkMode = 'consumer' | 'supplier'
export type SupplierStatus = 'not_applied' | 'pending' | 'active' | 'rejected' | 'suspended'
export interface UserCapabilities {
  consumer: boolean
  supplier_status: SupplierStatus
  marketplace_enabled: boolean
  allowed_workspaces: WorkMode[]
}

export interface SupplierProfile {
  status: SupplierStatus
  agreement_version: number
  application_fields: Record<string, string>
  review_reason?: string
  payout_frozen: boolean
}
export interface SupplierPriceTier { from_tokens: number; input_per_million: string; output_per_million: string; cache_read_per_million: string; cache_write_per_million: string }
export interface SupplierPrice { model: string; public_model: string; group_name: string; price_version: string | number; rate_multiplier: string; tiers: SupplierPriceTier[] }
export interface SupplierProvider { platform: string; label: string; account_types: ('apikey' | 'api_key' | 'oauth')[]; models: string[]; prices?: SupplierPrice[] }
export interface SupplierOptions { policy_version?: number; agreement_version: number; agreement_text?: string; agreement_url?: string; application_fields: string[]; max_supplier_concurrency: number; minimum_payout_usd?: string | null; fee_bps: number; hold_hours: number; providers: SupplierProvider[] }
export interface SupplierOffer { id: number; review_status: string; enabled: boolean; allowed_models: string[]; version: number }
export interface SupplierUsage { requests: number; input_tokens: number; output_tokens: number; consumer_charge: string; supplier_net: string }
export interface SupplierReview { status: string; reason?: string; created_at: string }
export interface SupplierAccount { id: number; name: string; platform: string; type: string; concurrency: number; status: string; offer: SupplierOffer; credential_status: string; current_concurrency: number; created_at: string; usage?: SupplierUsage | null; reviews?: SupplierReview[] | null }
export interface SupplierOverview { online_accounts: number; current_requests: number; pending: string; available: string; payout_reserved: string; updated_at: string; daily_usage?: { date: string; requests: number; supplier_net: string }[] | null }
export interface Page<T> { items: T[]; total: number; page: number; page_size: number; pages: number }
export interface CreateSupplierAccount { name: string; platform: string; type: string; api_key?: string; allowed_models: string[]; concurrency: number }

export interface SupplierAccountFilters { q: string; platform: string; status: string; model: string }

export interface SupplierWallet { pending: string; available: string; payout_reserved: string; debt: string; currency: string; updated_at: string }
export interface SupplierEarning { operation_id: string; date: string; account_id: number; account_name: string; model: string; consumer_charge: string; platform_fee: string; supplier_net: string; status: string; refund_amount?: string }
export interface EarningsPage extends Page<SupplierEarning> { wallet: SupplierWallet }
export interface SupplierStatement { id: number; month: string; version: number; opening_pending: string; opening_available: string; opening_reserved: string; opening_debt: string; closing_pending: string; closing_available: string; closing_reserved: string; closing_debt: string; debt_offset: string; earned_net: string; platform_fee: string; refund_net: string; payout_paid: string; cutoff_ledger_id: number; created_at: string; currency: string }
export interface PayoutDestination { id: number; masked_label: string; verified: boolean; version: number; created_at: string }
export interface SupplierPayout { id: number; supplier_id: number; destination_id: number; masked_label: string; amount: string; currency: string; status: string; external_reference?: string; paid_at?: string; created_at: string }

export interface StatementEntry { operation_id: string; date: string; account_id: number; account_name: string; model: string; kind: string; supplier_debt_offset: string; consumer_charge: string; platform_fee: string; supplier_net: string }
