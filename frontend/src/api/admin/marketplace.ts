import { apiClient } from '../client'
import type { Page } from '@/types/marketplace'

const base = '/admin/marketplace'
export interface Supplier { user_id: number; status: string; agreement_version: number; application_fields: Record<string, string>; review_reason?: string; payout_frozen: boolean; version: number }
export interface Offer { id: number; account_id: number; owner_user_id: number; account_name: string; platform: string; type: string; concurrency: number; review_status: string; enabled: boolean; allowed_models: string[]; version: number }
export interface Tier { from_tokens: number; input_per_million: string; output_per_million: string; cache_read_per_million: string; cache_write_per_million: string }
export interface Model { public_model: string; actual_model: string; context_limit: number; max_output_tokens: number; rate_multiplier: string; tiers: Tier[] }
export interface BookGroup { group_id: number; name: string; platform: string; protocol: 'chat_completions' | 'responses'; models: Model[] }
export interface Policy { version: number; fee_bps: number; hold_hours: number; max_supplier_concurrency: number; minimum_payout_usd: string }
export interface PolicyResult { version: number; published_at: string; policy: Policy; book: { groups: BookGroup[] } }
export interface Payout { id: number; supplier_id: number; status: string; amount: string; currency: string; paid_at?: string; external_reference?: string; evidence?: string; created_at?: string }
export interface Destination { id: number; supplier_id: number; verified: boolean; masked_label: string; version: number; verified_by?: number; verified_at?: string; verification_evidence?: string }
export interface Receipt { kind: string; id: string; status: string; age_seconds: number; difference?: string }
export interface ExecutionIncident { operation_id: string; receipt_id: string; attempt_id: number; release_operation_id: string; actor_user_id: number; quote_digest: string; evidence: string; created_at: string; status: 'recorded_manual_review' }
export interface ReconciliationTotals { receipt_statuses: Record<string, number>; unbalanced_transactions: number; wallet_differences: number; funding_differences: number; aged_outbox: number; uncertain_payouts: number }
export interface ReconciliationPage extends Page<Receipt> { totals: ReconciliationTotals }
export async function reconciliation(page: number, status: string): Promise<ReconciliationPage> { return (await apiClient.get<ReconciliationPage>(`${base}/reconciliation`, { params: { page, page_size: 20, status: status || undefined } })).data }
type List<T> = Page<T> | T[]
export const items = <T>(value: List<T>): T[] => Array.isArray(value) ? value : value.items
export async function list<T>(path: string, page = 1): Promise<List<T>> { return (await apiClient.get<List<T>>(`${base}/${path}`, { params: { page, page_size: 20 } })).data }
export async function get<T>(path: string): Promise<T> { return (await apiClient.get<T>(`${base}/${path}`)).data }
export async function post<T>(path: string, body: unknown, key?: string): Promise<T> { return (await apiClient.post<T>(`${base}/${path}`, body, key ? { headers: { 'Idempotency-Key': key } } : undefined)).data }
