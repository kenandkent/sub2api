import { createPinia } from 'pinia'
import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import PayoutDestinationsView from '../PayoutDestinationsView.vue'
const api = vi.hoisted(() => ({ listDestinations: vi.fn(), createDestination: vi.fn(), getDestinationOperation: vi.fn() }))
vi.mock('@/api/supplier', () => api)
vi.mock('@/stores/auth', () => ({ useAuthStore: () => ({ user: { id: 42 } }) }))
vi.mock('vue-i18n', async () => ({ ...(await vi.importActual<typeof import('vue-i18n')>('vue-i18n')), useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@/composables/useStepUp', () => ({ useStepUp: () => ({ run: async (fn: () => Promise<unknown>) => fn() }), isStepUpCancelled: () => false }))
beforeEach(() => { sessionStorage.clear(); vi.clearAllMocks(); api.listDestinations.mockResolvedValue([{ id: 1, masked_label: '****1234', verified: true, version: 2 }]) })
describe('payout destinations', () => {
  it('shows only masked details and clears a one-time credential after submission', async () => {
    api.createDestination.mockResolvedValue({})
    const w = mount(PayoutDestinationsView, { global: { plugins: [createPinia()], stubs: { AppLayout: { template: '<div><slot /></div>' }, TotpStepUpDialog: true, RouterLink: true } } }); await flushPromises()
    expect(w.text()).toContain('****1234')
    await w.get('[data-test=label]').setValue('Bank ****5678'); await w.get('[data-test=credential]').setValue('secret')
    await w.get('[data-test=save]').trigger('click'); await flushPromises()
    expect((w.get('[data-test=credential]').element as HTMLInputElement).value).toBe('')
    expect(w.text()).not.toContain('secret')
  })
  it('recovers an in-flight destination with the same key and a re-entered secret after reload', async () => {
    let resolve!: (value: unknown) => void; let retried: unknown; api.createDestination.mockImplementationOnce(() => new Promise(r => { resolve = r })).mockImplementationOnce(async (payload, key) => { retried = [{ ...payload }, key]; return {} })
    const settings = { global: { plugins: [createPinia()], stubs: { AppLayout: { template: '<div><slot /></div>' }, TotpStepUpDialog: true, RouterLink: true } } }
    const first = mount(PayoutDestinationsView, settings); await flushPromises()
    await first.get('[data-test=label]').setValue('Bank ****5678'); await first.get('[data-test=credential]').setValue('secret')
    await first.get('[data-test=save]').trigger('click'); await flushPromises()
    const key = api.createDestination.mock.calls[0][1]
    expect(sessionStorage.getItem('supplier-destination-operation:42')).toBe(key)
    expect(JSON.stringify(sessionStorage)).not.toContain('secret')
    first.unmount()
    const second = mount(PayoutDestinationsView, { ...settings, global: { ...settings.global, plugins: [createPinia()] } }); await flushPromises()
    await second.get('[data-test=credential]').setValue('secret')
    await second.get('[data-test=retry-destination]').trigger('click'); await flushPromises()
    expect(retried).toEqual([{ masked_label: 'Bank ****5678', credential: 'secret' }, key])
    resolve({}); first.unmount(); second.unmount()
  })
})
