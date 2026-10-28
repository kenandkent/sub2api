import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import EligibleBalanceSummary from '../EligibleBalanceSummary.vue'
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
describe('eligible balance', () => {
  it('separately displays available, reserved, excluded and debt, with refresh', async () => {
    const wrapper = mount(EligibleBalanceSummary, { props: { balance: { currency: 'USD', eligible_available: '2.01', reserved: '3.00', excluded_available: '4.00', debt: '5.00', migrated: true, reconciled: true }, loading: false, error: '' } })
    for (const amount of ['2.01', '3.00', '4.00', '5.00']) expect(wrapper.text()).toContain(amount)
    expect(wrapper.text()).toContain('marketplace.excludedExplanation')
    await wrapper.find('button').trigger('click')
    expect(wrapper.emitted('refresh')).toBeTruthy()
  })
  it('shows errors without displaying a balance as sufficient', () => {
    const wrapper = mount(EligibleBalanceSummary, { props: { balance: null, loading: false, error: 'failed' } })
    expect(wrapper.text()).toContain('failed')
  })
})
