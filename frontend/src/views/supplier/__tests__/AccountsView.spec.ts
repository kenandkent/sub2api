import { mount, flushPromises } from '@vue/test-utils'
import { describe, it, expect, vi, beforeEach } from 'vitest'
import AccountsView from '../AccountsView.vue'

const api = vi.hoisted(() => ({ listAccounts: vi.fn(), accountAction: vi.fn(), updateAccount: vi.fn(), getSupplierOptions: vi.fn() }))
vi.mock('@/api/supplier', () => api)
vi.mock('vue-i18n', async (importOriginal) => ({ ...await importOriginal<typeof import('vue-i18n')>(), useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('vue-router', async (importOriginal) => ({ ...await importOriginal<typeof import('vue-router')>(), useRouter: () => ({ push: vi.fn() }) }))
vi.mock('@/stores/workMode', () => ({ useWorkModeStore: () => ({ dirty: false }) }))
const account = { id: 1, name: 'Shared', platform: 'openai', type: 'api_key', concurrency: 1, status: 'active', credential_status: 'valid', current_concurrency: 0, created_at: '', offer: { id: 2, review_status: 'approved', enabled: true, allowed_models: ['model'], version: 1 } }
const stubs = { AppLayout: { template: '<div><slot /></div>' }, TablePageLayout: { template: '<div><slot name="actions"/><slot name="filters"/><slot name="table"/></div>' }, DataTable: { props: ['data'], template: '<div><div v-for="row in data" :key="row.id"><slot name="cell-review" :row="row"/><slot name="cell-health" :row="row"/><slot name="cell-actions" :row="row"/></div></div>' }, StatusBadge: { props: ['label'], template: '<span>{{ label }}</span>' }, ConfirmDialog: { props: ['show', 'message'], template: '<div v-if="show">{{ message }}<button @click="$emit(\'confirm\')">confirm</button></div>' }, AccountShareWizard: true, RouterLink: { template: '<a><slot /></a>' } }
describe('supplier accounts', () => {
  beforeEach(() => { vi.resetAllMocks(); api.listAccounts.mockResolvedValue({ items: [account], pages: 1 }); api.getSupplierOptions.mockResolvedValue({ providers: [], application_fields: [], max_supplier_concurrency: 4, agreement_version: 1, fee_bps: 1000, hold_hours: 24 }) })
  it('shows review and credential health independently and explains pause', async () => {
    const wrapper = mount(AccountsView, { global: { stubs } }); await flushPromises()
    expect(wrapper.text()).toContain('approved'); expect(wrapper.text()).toContain('valid')
    await wrapper.get('[data-test="pause"]').trigger('click')
    expect(wrapper.text()).toContain('marketplace.pauseExplanation')
  })
})

it('sends selected filters to the account list and resets pagination', async () => {
  const wrapper = mount(AccountsView, { global: { stubs } }); await flushPromises()
  await wrapper.get('[data-test="filter-q"]').setValue('team')
  await wrapper.get('[data-test="filter-platform"]').setValue('openai')
  await wrapper.get('[data-test="filter-status"]').setValue('approved')
  await wrapper.get('[data-test="filter-model"]').setValue('gpt')
  await wrapper.get('[data-test="apply-filters"]').trigger('submit'); await flushPromises()
  expect(api.listAccounts).toHaveBeenLastCalledWith(1, { q: 'team', platform: 'openai', status: 'approved', model: 'gpt' })
})
