import { mount, flushPromises } from '@vue/test-utils'
import { describe, it, expect, vi } from 'vitest'
import AccountDetailView from '../AccountDetailView.vue'
const api = vi.hoisted(() => ({ getAccount: vi.fn(), getSupplierOptions: vi.fn(), updateAccount: vi.fn(), accountAction: vi.fn() }))
vi.mock('@/api/supplier', () => api)
vi.mock('vue-i18n', async (original) => ({ ...await original<typeof import('vue-i18n')>(), useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('vue-router', async (original) => ({ ...await original<typeof import('vue-router')>(), useRoute: () => ({ params: { id: '1' } }) }))
vi.mock('@/stores/workMode', () => ({ useWorkModeStore: () => ({ dirty: false, capabilities: { supplier_status: 'active' } }) }))
const account = { id: 1, name: 'Original', platform: 'openai', type: 'api_key', concurrency: 1, status: 'active', credential_status: 'valid', current_concurrency: 0, created_at: '', offer: { id: 2, review_status: 'approved', enabled: true, allowed_models: ['model'], version: 1 } }
it('retains unsaved configuration when the server reports a version conflict', async () => {
  api.getAccount.mockResolvedValue(account)
  api.getSupplierOptions.mockResolvedValue({ providers: [{ platform: 'openai', models: ['model'] }], max_supplier_concurrency: 4 })
  api.updateAccount.mockRejectedValue({ status: 409 })
  const wrapper = mount(AccountDetailView, { global: { stubs: { AppLayout: { template: '<div><slot /></div>' }, RouterLink: { template: '<a><slot /></a>' }, ConfirmDialog: true } } })
  await flushPromises()
  await wrapper.get('input[required]').setValue('My changes')
  await wrapper.get('form').trigger('submit'); await flushPromises()
  expect(wrapper.text()).toContain('marketplace.versionConflict')
  expect((wrapper.get('input[required]').element as HTMLInputElement).value).toBe('My changes')
})
