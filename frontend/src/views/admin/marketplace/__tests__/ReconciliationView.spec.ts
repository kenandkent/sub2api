import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { webcrypto } from 'node:crypto'
import { reactive } from 'vue'
if (!globalThis.crypto.subtle) Object.defineProperty(globalThis.crypto, 'subtle', { value: webcrypto.subtle, configurable: true })
import ReconciliationView from '../ReconciliationView.vue'
import enFunding from '@/i18n/locales/en/adminFundingTools'
import zhFunding from '@/i18n/locales/zh/adminFundingTools'
const step = vi.hoisted(() => ({ run: vi.fn((fn: () => Promise<unknown>) => fn()) }))
const auth = vi.hoisted(() => ({ state: null as any }))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => auth.state }))
vi.mock('@/composables/useStepUp', () => ({ useStepUp: () => step, isStepUpCancelled: (e: any) => e?.code === 'STEP_UP_CANCELLED' }))
vi.mock('vue-i18n', async (importOriginal) => ({ ...(await importOriginal<typeof import('vue-i18n')>()), useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@/api/admin/marketplace', () => ({ reconciliation: vi.fn().mockResolvedValue({ items: [{ kind: 'receipt', id: 'r1', status: 'uncertain', age_seconds: 5 }, { kind: 'wallet', id: 'w1', status: 'different', age_seconds: 6 }, { kind: 'maturity', id: '42', status: 'retrying', age_seconds: 3600 }], total: 3, totals: { receipt_statuses: { uncertain: 1 }, unbalanced_transactions: 0, wallet_differences: 1, funding_differences: 0, aged_outbox: 0, uncertain_payouts: 0, maturity_failures: 2, maturity_oldest_failure_age_seconds: 3600 } }), get: vi.fn().mockResolvedValue({ items: [] }), post: vi.fn().mockResolvedValue({}) }))
const stubs = { AppLayout: { template: '<div><slot /></div>' }, TablePageLayout: { template: '<div><slot name="filters"/><slot name="table"/><slot name="pagination"/></div>' }, DataTable: { props: ['data'], template: '<div><div v-for="row in data" :key="row.id" :data-kind="row.kind"><slot name="cell-actions" :row="row"/></div></div>' }, StatCard: { props: ['title', 'value'], template: '<div>{{title}} {{value}}</div>' }, BaseDialog: { props: ['show'], template: '<div v-if="show"><slot/><slot name="footer"/></div>' }, TotpStepUpDialog: true, FundingAdminToolsDialog: { props: ['show'], template: `<div v-if="show" data-test="funding-tools-dialog"><button data-test="close-funding-tools" @click="$emit('close')">close</button></div>` } }
beforeEach(() => { auth.state = reactive({ user: { id: 42 } }); sessionStorage.clear(); vi.clearAllMocks(); step.run.mockImplementation((fn: () => Promise<unknown>) => fn()) })
describe('reconciliation', () => {
  it('shows recovery actions only for receipt rows and shows aggregate differences', async () => {
    const wrapper = mount(ReconciliationView, { global: { stubs } }); await flushPromises()
    expect(wrapper.find('[data-kind="receipt"]').text()).toContain('adminMarketplace.reconcile')
    expect(wrapper.find('[data-kind="wallet"]').findAll('button')).toHaveLength(0); expect(wrapper.find('[data-kind="maturity"]').findAll('button')).toHaveLength(0)
    expect(wrapper.text()).toContain('adminMarketplace.wallet_differences 1'); expect(wrapper.text()).toContain('adminMarketplace.maturity_failures 2'); expect(wrapper.text()).toContain('adminMarketplace.maturity_oldest_failure_age_seconds 3600')
  })
  it('requires step-up for a refund with its original reason and amount', async () => {
    step.run.mockClear()
    const wrapper = mount(ReconciliationView, { global: { stubs } }); await flushPromises()
    const api = await import('@/api/admin/marketplace'); vi.mocked(api.get).mockResolvedValueOnce({ receipt_id: '12345678-1234-1234-1234-123456789abc', status: 'settled', captured: '3.00', refunded_amount: '0', refundable_amount: '3.00', capture_status: 'pending' }); await wrapper.get('[data-test=receipt-id]').setValue('12345678-1234-1234-1234-123456789abc'); wrapper.get('[data-test=receipt-id]').element.closest('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true })); await flushPromises(); step.run.mockClear(); await wrapper.get('[data-test=refund-loaded]').trigger('click')
    await wrapper.find('input[inputmode="decimal"]').setValue('1.00'); await wrapper.find('textarea').setValue('Adjustment evidence')
    await wrapper.findAll('button').find(b => b.text() === 'adminMarketplace.confirmAction')!.trigger('click'); await flushPromises()
    await vi.waitFor(() => expect(step.run).toHaveBeenCalledOnce())
    expect(wrapper.findAll('button').find(b => b.text() === 'adminMarketplace.confirmAction')!.attributes('disabled')).toBeDefined()
  })

  it('looks up a settled receipt before refund and shows its refundable amount', async () => {
    const api = await import('@/api/admin/marketplace'); vi.mocked(api.get).mockResolvedValueOnce({ receipt_id: '12345678-1234-1234-1234-123456789abc', status: 'settled', captured: '5.00', refunded_amount: '1.00', refundable_amount: '4.00', actual_charge: '5.00', capture_status: 'matured' })
    const w = mount(ReconciliationView, { global: { stubs } }); await flushPromises()
    await w.get('[data-test=receipt-id]').setValue('12345678-1234-1234-1234-123456789abc')
    await w.get('[data-test=receipt-id]').element.closest('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true })); await flushPromises()
    expect(w.text()).toContain('4.00'); await w.get('[data-test=refund-loaded]').trigger('click')
    expect(w.find('input[inputmode="decimal"]').exists()).toBe(true)
  })
  it('retries an uncertain refund after re-entering the identical body without saving it', async () => {
    sessionStorage.clear(); const api = await import('@/api/admin/marketplace'); vi.mocked(api.post).mockRejectedValueOnce(new Error('network')).mockResolvedValueOnce({})
    const id = '12345678-1234-1234-1234-123456789abc'; const detail = { receipt_id: id, status: 'settled', captured: '5.00', refunded_amount: '1.00', refundable_amount: '4.00', actual_charge: '5.00', capture_status: 'matured' }
    vi.mocked(api.get).mockResolvedValue(detail)
    const first = mount(ReconciliationView, { global: { stubs } }); await flushPromises(); await first.get('[data-test=receipt-id]').setValue(id); first.get('[data-test=receipt-id]').element.closest('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true })); await flushPromises(); await first.get('[data-test=refund-loaded]').trigger('click'); await first.find('input[inputmode=decimal]').setValue('1.00'); await first.find('textarea').setValue('Verified evidence'); await first.findAll('button').find(b => b.text() === 'adminMarketplace.confirmAction')!.trigger('click'); await flushPromises()
    await vi.waitFor(() => expect(vi.mocked(api.post)).toHaveBeenCalledTimes(1)); const sent = vi.mocked(api.post).mock.calls.at(-1); const count = vi.mocked(api.post).mock.calls.length; expect(JSON.stringify(sessionStorage)).toContain(sent![2]); expect(JSON.stringify(sessionStorage)).not.toContain('1.00'); expect(JSON.stringify(sessionStorage)).not.toContain('Verified evidence'); first.unmount()
    const second = mount(ReconciliationView, { global: { stubs } }); await flushPromises(); await second.get('[data-test=receipt-id]').setValue(id); second.get('[data-test=receipt-id]').element.closest('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true })); await flushPromises(); await second.get('[data-test=refund-loaded]').trigger('click'); await second.findAll('button').find(b => b.text() === 'adminMarketplace.checkStatus')!.trigger('click'); await flushPromises(); await second.find('input[inputmode=decimal]').setValue('1.00'); await second.find('textarea').setValue('Wrong evidence'); await second.findAll('button').find(b => b.text() === 'adminMarketplace.retryAction')!.trigger('click'); await vi.waitFor(() => expect(second.text()).toContain('adminMarketplace.retryPayloadMismatch')); expect(vi.mocked(api.post).mock.calls).toHaveLength(count); await second.find('textarea').setValue('Verified evidence'); await second.findAll('button').find(b => b.text() === 'adminMarketplace.retryAction')!.trigger('click'); await vi.waitFor(() => expect(vi.mocked(api.post).mock.calls).toHaveLength(count + 1)); await flushPromises()
    expect(vi.mocked(api.post).mock.calls.at(-1)).toEqual(sent); second.unmount(); sessionStorage.clear()
  })
  it('does not display a receipt lookup returned after logout', async () => {
    const api = await import('@/api/admin/marketplace'); let resolve!: (value: unknown) => void; vi.mocked(api.get).mockImplementationOnce(() => new Promise(r => { resolve = r }))
    const w = mount(ReconciliationView, { global: { stubs } }); await flushPromises(); await w.get('[data-test=receipt-id]').setValue('12345678-1234-1234-1234-123456789abc'); w.get('[data-test=receipt-id]').element.closest('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true })); await flushPromises(); auth.state.user = null; await flushPromises(); resolve({ receipt_id: '12345678-1234-1234-1234-123456789abc', status: 'settled', captured: 'private-amount', refundable_amount: '4', capture_status: 'matured' }); await flushPromises(); expect(w.text()).not.toContain('private-amount'); w.unmount()
  })

  it('keeps an earlier uncertain refund after cancelling a retry step-up', async () => {
    const api = await import('@/api/admin/marketplace'); vi.mocked(api.post).mockRejectedValueOnce(new Error('network')).mockResolvedValueOnce({})
    const id = '12345678-1234-1234-1234-123456789abc'; vi.mocked(api.get).mockResolvedValue({ receipt_id: id, status: 'settled', captured: '5', refunded_amount: '0', refundable_amount: '5', actual_charge: '5', capture_status: 'pending' })
    const w = mount(ReconciliationView, { global: { stubs } }); await flushPromises(); await w.get('[data-test=receipt-id]').setValue(id); w.get('[data-test=receipt-id]').element.closest('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true })); await flushPromises(); await w.get('[data-test=refund-loaded]').trigger('click'); await w.find('input[inputmode=decimal]').setValue('1.00'); await w.find('textarea').setValue('proof'); await w.findAll('button').find(b => b.text() === 'adminMarketplace.confirmAction')!.trigger('click'); await vi.waitFor(() => expect(api.post).toHaveBeenCalledTimes(1)); const key = vi.mocked(api.post).mock.calls[0][2]
    await w.findAll('button').find(b => b.text() === 'adminMarketplace.checkStatus')!.trigger('click'); await flushPromises(); step.run.mockRejectedValueOnce({ code: 'STEP_UP_CANCELLED' }); await w.findAll('button').find(b => b.text() === 'adminMarketplace.retryAction')!.trigger('click'); await vi.waitFor(() => expect(step.run).toHaveBeenCalledTimes(4)); await flushPromises(); expect(w.findAll('button').find(b => b.text() === 'adminMarketplace.retryAction')?.attributes('disabled')).toBeDefined(); expect(JSON.stringify(sessionStorage)).toContain(key)
    await w.findAll('button').find(b => b.text() === 'adminMarketplace.checkStatus')!.trigger('click'); await flushPromises(); await w.findAll('button').find(b => b.text() === 'adminMarketplace.retryAction')!.trigger('click'); await vi.waitFor(() => expect(api.post).toHaveBeenCalledTimes(2)); expect(vi.mocked(api.post).mock.calls[1][2]).toBe(key); w.unmount()
  })

  it('opens and closes the audited funding tools from reconciliation', async () => {
    const w = mount(ReconciliationView, { global: { stubs } }); await flushPromises()
    expect(w.find('[data-test=funding-tools-dialog]').exists()).toBe(false)
    await w.get('[data-test=open-funding-tools]').trigger('click')
    expect(w.find('[data-test=funding-tools-dialog]').exists()).toBe(true)
    await w.get('[data-test=close-funding-tools]').trigger('click')
    expect(w.find('[data-test=funding-tools-dialog]').exists()).toBe(false)
    w.unmount()
  })

  it('mounts the funding tools and lets an administrator select vault rotation', async () => {
    const realStubs = { ...stubs }; delete (realStubs as Partial<typeof stubs>).FundingAdminToolsDialog
    const w = mount(ReconciliationView, { global: { stubs: realStubs } }); await flushPromises()
    await w.get('[data-test=open-funding-tools]').trigger('click'); await flushPromises()
    await w.get('[data-test=tab-vault]').trigger('click')
    expect(w.text()).toContain('adminFundingTools.vaultTitle')
    expect(w.find('[data-test=vault-submit]').exists()).toBe(true)
    w.unmount()
  })

  it('provides labels for each funding tool tab in both locales', () => {
    for (const key of ['tab_reclass', 'tab_risk', 'tab_vault'] as const) { expect(enFunding[key].trim()).toBeTruthy(); expect(zhFunding[key].trim()).toBeTruthy() }
  })

})
