import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import SharedGroupPriceDetails from '../SharedGroupPriceDetails.vue'
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
describe('shared pricing', () => {
  it('shows tiers and exact zero, missing, cache read and cache write prices', () => {
    const wrapper = mount(SharedGroupPriceDetails, { props: { groups: [{ group_id: 1, name: 'Shared', platform: 'openai', protocol: 'openai', models: [{ public_model: 'model', actual_model: 'actual', context_limit: 100, max_output_tokens: 20, rate_multiplier: '1.000000000000000001', tiers: [{ from_tokens: 0, input_per_million: '0', output_per_million: '12345678901234567890.123456', cache_read_per_million: '0.000001', cache_write_per_million: '' }, { from_tokens: 100, input_per_million: '1', output_per_million: '2', cache_read_per_million: '3', cache_write_per_million: '4' }] }] }] } })
    expect(wrapper.text()).toContain('12345678901234567890.123456')
    expect(wrapper.text()).toContain('0.000001')
    expect(wrapper.text()).toContain('marketplace.priceMissing')
    expect(wrapper.text()).toContain('100')
    expect(wrapper.text()).toContain('$0')
  })
})
