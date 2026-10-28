import { flushPromises, mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import EarningsView from '../EarningsView.vue'
const api = vi.hoisted(() => ({ getEarnings: vi.fn() }))
vi.mock('@/api/supplier', () => api)
vi.mock('vue-i18n', async () => ({ ...(await vi.importActual<typeof import('vue-i18n')>('vue-i18n')), useI18n: () => ({ t: (key: string) => key }) }))
describe('earnings', () => {
  it('shows immutable earnings and related refund separately, and applies filters', async () => {
    api.getEarnings.mockResolvedValue({ items: [{ operation_id: 'op-1', date: '2026-01-01', account_id: 3, account_name: 'Account', model: 'model', consumer_charge: '0.12345678', platform_fee: '0.02345678', supplier_net: '0.10000000', status: 'pending', refund_amount: '0.01' }], page: 1, pages: 1, wallet: { pending: '0.1', available: '3', payout_reserved: '4', debt: '0', currency: 'USD' } })
    const w = mount(EarningsView, { global: { stubs: { AppLayout: { template: '<div><slot /></div>' }, TablePageLayout: { template: '<div><slot name="filters"/><slot name="table"/></div>' } } } }); await flushPromises()
    expect(w.text()).toContain('0.12345678'); expect(w.text()).toContain('0.01'); expect(w.text()).toContain('op-1')
    await w.get('[data-test=model]').setValue('model'); await w.get('[data-test=filter]').trigger('click'); await flushPromises()
    expect(api.getEarnings).toHaveBeenLastCalledWith(1, expect.objectContaining({ model: 'model' }))
  })
})
