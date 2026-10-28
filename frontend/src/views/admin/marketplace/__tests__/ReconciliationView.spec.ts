import { describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import ReconciliationView from '../ReconciliationView.vue'
const step = vi.hoisted(() => ({ run: vi.fn((fn: () => Promise<unknown>) => fn()) }))
vi.mock('@/composables/useStepUp', () => ({ useStepUp: () => step, isStepUpCancelled: () => false }))
vi.mock('vue-i18n', async (importOriginal) => ({ ...(await importOriginal<typeof import('vue-i18n')>()), useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@/api/admin/marketplace', () => ({ reconciliation: vi.fn().mockResolvedValue({ items: [{ kind: 'receipt', id: 'r1', status: 'uncertain', age_seconds: 5 }, { kind: 'wallet', id: 'w1', status: 'different', age_seconds: 6 }], total: 2, totals: { receipt_statuses: { uncertain: 1 }, unbalanced_transactions: 0, wallet_differences: 1, funding_differences: 0, aged_outbox: 0, uncertain_payouts: 0 } }), get: vi.fn().mockResolvedValue({ items: [] }), post: vi.fn().mockResolvedValue({}) }))
const stubs = { AppLayout: { template: '<div><slot /></div>' }, TablePageLayout: { template: '<div><slot name="filters"/><slot name="table"/><slot name="pagination"/></div>' }, DataTable: { props: ['data'], template: '<div><div v-for="row in data" :key="row.id" :data-kind="row.kind"><slot name="cell-actions" :row="row"/></div></div>' }, StatCard: { props: ['title', 'value'], template: '<div>{{title}} {{value}}</div>' }, BaseDialog: { props: ['show'], template: '<div v-if="show"><slot/><slot name="footer"/></div>' }, TotpStepUpDialog: true }
describe('reconciliation', () => {
  it('shows recovery actions only for receipt rows and shows aggregate differences', async () => {
    const wrapper = mount(ReconciliationView, { global: { stubs } }); await flushPromises()
    expect(wrapper.find('[data-kind="receipt"]').text()).toContain('adminMarketplace.reconcile')
    expect(wrapper.find('[data-kind="wallet"]').findAll('button')).toHaveLength(0)
    expect(wrapper.text()).toContain('adminMarketplace.wallet_differences 1')
  })
  it('requires step-up for a refund with its original reason and amount', async () => {
    step.run.mockClear()
    const wrapper = mount(ReconciliationView, { global: { stubs } }); await flushPromises()
    await wrapper.find('[data-kind="receipt"]').findAll('button').find(b => b.text() === 'adminMarketplace.refund')!.trigger('click')
    await wrapper.find('input[inputmode="decimal"]').setValue('1.00'); await wrapper.find('textarea').setValue('Adjustment evidence')
    await wrapper.findAll('button').find(b => b.text() === 'adminMarketplace.confirmAction')!.trigger('click'); await flushPromises()
    expect(step.run).toHaveBeenCalledOnce()
    expect(wrapper.findAll('button').find(b => b.text() === 'adminMarketplace.confirmAction')!.attributes('disabled')).toBeDefined()
  })

})
