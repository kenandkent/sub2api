import { mount, flushPromises } from '@vue/test-utils'
import { beforeEach, it, expect, vi } from 'vitest'
import AccountDetailView from '../AccountDetailView.vue'
const api = vi.hoisted(() => ({ getAccount: vi.fn(), getSupplierOptions: vi.fn(), updateAccount: vi.fn(), accountAction: vi.fn() }))
vi.mock('@/api/supplier', () => api)
vi.mock('vue-i18n', async (original) => ({ ...await original<typeof import('vue-i18n')>(), useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('vue-router', async (original) => ({ ...await original<typeof import('vue-router')>(), useRoute: () => ({ params: { id: '1' } }), onBeforeRouteLeave: vi.fn(), onBeforeRouteUpdate: vi.fn() }))
const store = vi.hoisted(() => ({ dirty: false, capabilities: { supplier_status: 'active' }, confirmLeave: vi.fn(async () => { store.dirty = false; return true }) }))
vi.mock('@/stores/workMode', () => ({ useWorkModeStore: () => store }))
beforeEach(() => { vi.resetAllMocks(); store.dirty = false; store.confirmLeave.mockImplementation(async () => { store.dirty = false; return true }); api.getSupplierOptions.mockResolvedValue({ providers: [{ platform: 'openai', models: ['model'] }], max_supplier_concurrency: 4 }) })
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

it('displays aggregated usage and review history without showing credentials', async () => {
  api.getAccount.mockResolvedValue({ ...account, usage: { requests: 3, input_tokens: 5, output_tokens: 7, consumer_charge: '0.12345678', supplier_net: '0.10000000' }, reviews: [{ status: 'rejected', reason: 'Fix models', created_at: '2026-01-01' }] })
  const wrapper = mount(AccountDetailView, { global: { stubs: { AppLayout: { template: '<div><slot /></div>' }, RouterLink: { template: '<a><slot /></a>' }, ConfirmDialog: true } } }); await flushPromises()
  expect(wrapper.text()).toContain('0.12345678')
  expect(wrapper.text()).toContain('Fix models')
})

it('discards edits after confirming an action even if its second confirmation is canceled', async () => {
  api.getAccount.mockResolvedValue(account)
  const wrapper = mount(AccountDetailView, { global: { stubs: { AppLayout: { template: '<div><slot /></div>' }, RouterLink: { template: '<a><slot /></a>' }, ConfirmDialog: true } } }); await flushPromises()
  await wrapper.get('input[required]').setValue('Unsaved')
  await wrapper.findAll('button').find(b => b.text() === 'marketplace.pause')!.trigger('click'); await flushPromises()
  expect((wrapper.get('input[required]').element as HTMLInputElement).value).toBe('Original')
})

it('disables editing while a save is in flight', async () => {
  let resolve!: (value: unknown) => void
  api.getAccount.mockResolvedValue(account); api.updateAccount.mockReturnValue(new Promise(r => { resolve = r }))
  const wrapper = mount(AccountDetailView, { global: { stubs: { AppLayout: { template: '<div><slot /></div>' }, RouterLink: { template: '<a><slot /></a>' }, ConfirmDialog: true } } }); await flushPromises()
  await wrapper.get('input[required]').setValue('Changed'); await wrapper.get('form').trigger('submit')
  expect(wrapper.get('input[required]').attributes('disabled')).toBeDefined()
  resolve(account); await flushPromises()
})
it('offers credential replacement for the current account', async () => {
  api.getAccount.mockResolvedValue(account)
  const wrapper = mount(AccountDetailView, { global: { stubs: { AppLayout: { template: '<div><slot /></div>' }, RouterLink: { template: '<a><slot /></a>' }, ConfirmDialog: true, AccountCredentialDialog: { props: ['account'], template: '<div data-test="credential-dialog">{{ account.id }}</div>' } } } }); await flushPromises()
  await wrapper.get('[data-test="replace-credentials"]').trigger('click')
  expect(wrapper.get('[data-test="credential-dialog"]').text()).toBe('1')
})
it('localizes credential and review states separately', async () => {
  api.getAccount.mockResolvedValue(account)
  const wrapper = mount(AccountDetailView, { global: { stubs: { AppLayout: { template: '<div><slot /></div>' }, RouterLink: { template: '<a><slot /></a>' }, ConfirmDialog: true } } }); await flushPromises()
  expect(wrapper.text()).toContain('marketplace.review_approved')
  expect(wrapper.text()).toContain('marketplace.credential_valid')
})

it('keeps a timed-out update locked until a read confirms the submitted values', async () => {
  api.getAccount.mockResolvedValueOnce(account).mockResolvedValueOnce({ ...account, name: 'Changed', offer: { ...account.offer, version: 2 } })
  api.updateAccount.mockRejectedValue({ code: 'ETIMEDOUT' })
  const wrapper = mount(AccountDetailView, { global: { stubs: { AppLayout: { template: '<div><slot /></div>' }, RouterLink: { template: '<a><slot /></a>' }, ConfirmDialog: true } } }); await flushPromises()
  await wrapper.get('input[required]').setValue('Changed'); await wrapper.get('form').trigger('submit'); await flushPromises()
  expect(wrapper.text()).toContain('marketplace.operationUnknown')
  expect(wrapper.get('input[required]').attributes('disabled')).toBeDefined()
  await wrapper.get('[data-test="check-save"]').trigger('click'); await flushPromises()
  expect(wrapper.text()).not.toContain('marketplace.operationUnknown')
  expect(api.updateAccount).toHaveBeenCalledTimes(1)
})

it('retries the same CAS update after an unchanged reread', async () => {
  api.getAccount.mockResolvedValue(account); api.updateAccount.mockRejectedValueOnce({ code: 'ETIMEDOUT' }).mockResolvedValueOnce({ ...account, name: 'Changed', offer: { ...account.offer, version: 2 } })
  const wrapper = mount(AccountDetailView, { global: { stubs: { AppLayout: { template: '<div><slot /></div>' }, RouterLink: { template: '<a><slot /></a>' }, ConfirmDialog: true } } }); await flushPromises()
  await wrapper.get('input[required]').setValue('Changed'); await wrapper.get('form').trigger('submit'); await flushPromises()
  await wrapper.get('[data-test="check-save"]').trigger('click'); await flushPromises()
  expect(wrapper.text()).toContain('marketplace.operationUnknown')
  await wrapper.get('[data-test="retry-save"]').trigger('click'); await flushPromises()
  expect(api.updateAccount).toHaveBeenCalledTimes(2); expect(api.updateAccount.mock.calls[1]).toEqual(api.updateAccount.mock.calls[0]); expect(wrapper.text()).not.toContain('marketplace.operationUnknown')
})
it('unlocks to a conflict while preserving edits if a different update advanced the version', async () => {
  api.getAccount.mockResolvedValueOnce(account).mockResolvedValueOnce({ ...account, name: 'Other', offer: { ...account.offer, version: 2 } }); api.updateAccount.mockRejectedValue({ code: 'ETIMEDOUT' })
  const wrapper = mount(AccountDetailView, { global: { stubs: { AppLayout: { template: '<div><slot /></div>' }, RouterLink: { template: '<a><slot /></a>' }, ConfirmDialog: true } } }); await flushPromises()
  await wrapper.get('input[required]').setValue('Changed'); await wrapper.get('form').trigger('submit'); await flushPromises(); await wrapper.get('[data-test="check-save"]').trigger('click'); await flushPromises()
  expect(wrapper.text()).toContain('marketplace.versionConflict'); expect(wrapper.text()).not.toContain('marketplace.operationUnknown'); expect(wrapper.text()).toContain('Other'); expect(wrapper.text()).toContain('Changed'); expect((wrapper.get('input[required]').element as HTMLInputElement).value).toBe('Changed'); await wrapper.get('[data-test="ack-detail-conflict"]').trigger('click'); expect((wrapper.get('input[required]').element as HTMLInputElement).value).toBe('Changed')
})
