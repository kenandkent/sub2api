import { apiClient } from './client'
import type { UserCapabilities } from '@/types/marketplace'

export async function getCapabilities(signal?: AbortSignal): Promise<UserCapabilities> {
  const { data } = await apiClient.get<UserCapabilities>('/user/capabilities', { signal })
  return data
}

import type { SupplierProfile, SupplierOptions, SupplierAccount, SupplierOverview, Page, CreateSupplierAccount, SupplierAccountFilters } from '@/types/marketplace'
export async function getSupplierProfile(): Promise<SupplierProfile> { return (await apiClient.get('/supplier/profile')).data }
export async function submitSupplierProfile(payload: { agreement_version: number; fields: Record<string, string> }): Promise<SupplierProfile> { return (await apiClient.post('/supplier/profile', payload)).data }
export async function getSupplierOptions(): Promise<SupplierOptions> { return (await apiClient.get('/supplier/options')).data }
export async function getSupplierOverview(): Promise<SupplierOverview> { return (await apiClient.get('/supplier/overview')).data }
export async function listAccounts(page = 1, filters?: SupplierAccountFilters): Promise<Page<SupplierAccount>> { return (await apiClient.get('/supplier/accounts', { params: { page, page_size: 20, ...filters } })).data }
export async function getAccount(id: number): Promise<SupplierAccount> { return (await apiClient.get(`/supplier/accounts/${id}`)).data }
export async function createAccount(payload: CreateSupplierAccount): Promise<SupplierAccount> { return (await apiClient.post('/supplier/accounts', payload)).data }
export async function updateAccount(id: number, payload: { expected_version: number; name: string; concurrency: number; allowed_models: string[] }): Promise<SupplierAccount> { return (await apiClient.patch(`/supplier/accounts/${id}`, payload)).data }
export async function accountAction(id: number, action: 'submit' | 'pause' | 'resume' | 'revoke' | 'probe', version: number): Promise<unknown> { return (await apiClient.post(`/supplier/accounts/${id}/${action}`, { expected_version: version })).data }
export async function createAuthorization(payload: { purpose: 'create' | 'reauthorize'; provider: string; account_id?: number }): Promise<{ auth_url: string; session_id: string }> { return (await apiClient.post('/supplier/authorizations', payload)).data }
export async function completeAuthorization(id: string, payload: { state: string; code: string; purpose: 'create' | 'reauthorize'; account_id?: number }): Promise<{ account_id: number }> { return (await apiClient.post(`/supplier/authorizations/${encodeURIComponent(id)}/complete`, payload)).data }
