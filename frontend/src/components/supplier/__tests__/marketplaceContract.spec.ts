import { mount, flushPromises } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import AccountShareWizard from '../AccountShareWizard.vue'
import AccountDetailView from '@/views/supplier/AccountDetailView.vue'
import { supplierRedirect } from '@/router/workspace'
const api = vi.hoisted(() => ({ createAccount: vi.fn(), createAuthorization: vi.fn(), completeAuthorization: vi.fn(), getAccount: vi.fn(), updateAccount: vi.fn(), getAuthorizationStatus: vi.fn(), getSupplierOperation: vi.fn(), getSupplierOptions: vi.fn(), accountAction: vi.fn() }))
vi.mock('@/api/supplier', () => api)
vi.mock('vue-i18n', async (original) => ({ ...await original<typeof import('vue-i18n')>(), useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('vue-router', async (original) => ({ ...await original<typeof import('vue-router')>(), useRoute: () => ({ params: { id: '1' } }), onBeforeRouteLeave: vi.fn(), onBeforeRouteUpdate: vi.fn() }))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => ({ user: { id: 42 } }) }))
vi.mock('@/stores/workMode', () => ({ useWorkModeStore: () => ({ dirty: false, capabilities: { supplier_status: 'active' }, confirmLeave: async () => true }) }))
const opts = { providers: [{ platform: 'openai', label: 'OpenAI', account_types: ['apikey'], models: ['model'], prices: [{ model: 'model', public_model: 'model', group_name: 'pool', price_version: 1, rate_multiplier: '1', tiers: [{ from_tokens: 0, input_per_million: '1', output_per_million: '2', cache_read_per_million: '0.1', cache_write_per_million: '0.2' }] }] }], application_fields: [], agreement_text: 'Terms', max_supplier_concurrency: 4, agreement_version: 1, fee_bps: 1000, hold_hours: 24 }
const stubs = { BaseDialog: { template: '<div><slot/></div>' }, AppLayout: { template: '<div><slot /></div>' }, RouterLink: { template: '<a><slot /></a>' }, ConfirmDialog: true }
describe('observed contract mismatches', () => {
 it('backend apikey option accepts a key and creates the account', async () => {
  api.createAuthorization.mockResolvedValue({auth_url:'https://example.test/auth', session_id:'s1'})
  const wrapper = mount(AccountShareWizard, { props: { options: opts as any }, global: { stubs } })
  expect(wrapper.find('[data-test="api-key"]').exists()).toBe(true)
  await wrapper.get('[data-test="api-key"]').setValue('secret')
  expect(wrapper.get('[data-test="next"]').attributes('disabled')).toBeUndefined()
  await wrapper.get('[data-test="next"]').trigger('click')
  await wrapper.get('[data-test="name"]').setValue('Example')
  await wrapper.get('input[type="checkbox"]').setValue(true)
  await wrapper.get('[data-test="next"]').trigger('click')
  await wrapper.get('input[type="checkbox"]').setValue(true)
  await wrapper.get('form').trigger('submit'); await flushPromises()
  expect(api.createAccount).toHaveBeenCalledWith(expect.objectContaining({type:'apikey', api_key:'secret'}), expect.any(String))
  expect(api.createAuthorization).not.toHaveBeenCalled()
  wrapper.unmount()
 })
 it('backend apikey account offers credential rotation', async () => {
  api.getAccount.mockResolvedValue({ id: 1, name: 'Original', platform: 'openai', type: 'apikey', concurrency: 1, status: 'active', credential_status: 'valid', current_concurrency: 0, created_at: '', offer: { id: 2, review_status: 'approved', enabled: true, allowed_models: ['model'], version: 1 } })
  api.getSupplierOptions.mockResolvedValue(opts)
  const wrapper = mount(AccountDetailView, { global: { stubs } }); await flushPromises()
  expect(wrapper.find('[data-test="replace-credentials"]').exists()).toBe(true)
  wrapper.unmount()
 })
 it('accepts a valid relative agreement URL', async () => {
  const wrapper = mount(AccountShareWizard, { props: { options: { ...opts, providers: [{ ...opts.providers[0], account_types: ['api_key'] }], agreement_text: '', agreement_url: '/terms' } as any }, global: { stubs } })
  await wrapper.get('[data-test="api-key"]').setValue('secret'); await wrapper.get('[data-test="next"]').trigger('click')
  await wrapper.get('[data-test="name"]').setValue('Example'); await wrapper.get('input[type="checkbox"]').setValue(true)
  await wrapper.get('[data-test="next"]').trigger('click')
  expect(wrapper.text()).not.toContain('marketplace.agreementUnavailable')
  await wrapper.get('input[type="checkbox"]').setValue(true)
  expect(wrapper.get('[data-test="create"]').attributes('disabled')).toBeUndefined()
  wrapper.unmount()
 })
 it('master-off retains existing supplier history', () => {
  expect(supplierRedirect('/supplier/statements', { marketplace_enabled: false, supplier_status: 'active', allowed_workspaces: ['consumer','supplier'], consumer: true })).toBeNull()
 })
})
