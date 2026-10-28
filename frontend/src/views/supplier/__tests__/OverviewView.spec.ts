import { mount, flushPromises } from '@vue/test-utils'
import { it, expect, vi } from 'vitest'
import OverviewView from '../OverviewView.vue'
const api = vi.hoisted(() => ({ getSupplierOverview: vi.fn() }))
vi.mock('@/api/supplier', () => api)
vi.mock('vue-i18n', async (original) => ({ ...await original<typeof import('vue-i18n')>(), useI18n: () => ({ t: (key: string) => key }) }))
const stubs = { AppLayout: { template: '<div><slot /></div>' }, RouterLink: { template: '<a><slot /></a>' }, StatCard: { props: ['title', 'value'], template: '<div>{{ title }} {{ value }}</div>' } }
it('shows daily usage exactly as returned and a clear empty state when absent', async () => {
  api.getSupplierOverview.mockResolvedValueOnce({ online_accounts: 1, current_requests: 2, pending: '0.12345678', available: '0.01000000', payout_reserved: '0.00000000', updated_at: 'now', daily_usage: [{ date: '2026-01-01', requests: 4, supplier_net: '0.01234567' }] })
  const wrapper = mount(OverviewView, { global: { stubs } }); await flushPromises()
  expect(wrapper.text()).toContain('0.01234567'); expect(wrapper.text()).toContain('2026-01-01')
  api.getSupplierOverview.mockResolvedValueOnce({ online_accounts: 0, current_requests: 0, pending: '0', available: '0', payout_reserved: '0', updated_at: 'now' })
  const empty = mount(OverviewView, { global: { stubs } }); await flushPromises()
  expect(empty.text()).toContain('marketplace.noDailyUsage')
})
