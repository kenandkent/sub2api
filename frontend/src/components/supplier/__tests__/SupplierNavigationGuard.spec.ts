import { mount, flushPromises } from '@vue/test-utils'
import { createRouter, createMemoryHistory, RouterView } from 'vue-router'
import { it, expect, vi } from 'vitest'
import AccountShareWizard from '../AccountShareWizard.vue'
const api = vi.hoisted(() => ({ createAccount: vi.fn() }))
vi.mock('@/api/supplier', () => api)
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@/stores/workMode', () => ({ useWorkModeStore: () => ({ dirty: false, confirmLeave: async () => true }) }))
it('prevents actual router navigation while creation is pending', async () => {
  let resolve!: (value: unknown) => void; api.createAccount.mockReturnValue(new Promise(r => { resolve = r }))
  const options = { providers: [{ platform: 'openai', label: 'OpenAI', account_types: ['api_key'] as ('api_key' | 'oauth')[], models: ['model'], prices: [{ model: 'model', public_model: 'model', group_name: 'pool', price_version: 'v1', rate_multiplier: '1', tiers: [{ from_tokens: 0, input_per_million: '1', output_per_million: '1', cache_read_per_million: '1', cache_write_per_million: '1' }] }] }], application_fields: [], agreement_text: 'Terms', max_supplier_concurrency: 4, agreement_version: 1, fee_bps: 0, hold_hours: 0 }
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/wizard', component: AccountShareWizard, props: { options } }, { path: '/other', component: { template: '<div>other</div>' } }] })
  await router.push('/wizard'); await router.isReady()
  const wrapper = mount(RouterView, { global: { plugins: [router], stubs: { BaseDialog: { template: '<div><slot /></div>' }, RouterLink: { template: '<a><slot /></a>' } } } })
  await wrapper.get('[data-test="api-key"]').setValue('secret'); await wrapper.get('[data-test="next"]').trigger('click'); await wrapper.get('[data-test="name"]').setValue('name'); await wrapper.get('[type=checkbox]').setValue(true); await wrapper.get('[data-test="next"]').trigger('click'); await wrapper.get('[type=checkbox]').setValue(true)
  await wrapper.get('form').trigger('submit'); await router.push('/other')
  expect(router.currentRoute.value.path).toBe('/wizard')
  resolve({}); await flushPromises(); await router.push('/other')
  expect(router.currentRoute.value.path).toBe('/other')
})
