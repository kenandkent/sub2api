import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { reactive } from 'vue'
import PoliciesView from '../PoliciesView.vue'

const state = vi.hoisted(() => ({ workMode: { dirty: false, confirmLeave: vi.fn() }, get: vi.fn(), post: vi.fn(), leave: null as null | (() => boolean) }))
vi.mock('@/stores/workMode', () => ({ useWorkModeStore: () => state.workMode }))
vi.mock('vue-router', () => ({ onBeforeRouteLeave: (guard: () => boolean) => { state.leave = guard } }))
vi.mock('vue-i18n', async original => ({ ...await original<typeof import('vue-i18n')>(), useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@/api/admin/marketplace', () => ({ get: state.get, post: state.post }))
vi.mock('@/api/admin/groups', async original => ({ ...await original<typeof import('@/api/admin/groups')>(), getAll: vi.fn().mockResolvedValue([]) }))
vi.mock('@/composables/useStepUp', () => ({ useStepUp: () => ({ run: (fn: () => Promise<unknown>) => fn() }), isStepUpCancelled: () => false }))
const fixture = () => ({ version: 1, published_at: '', policy: { fee_bps: 2000, hold_hours: 24, max_supplier_concurrency: 3, minimum_payout_usd: '1.00' }, book: { groups: [{ group_id: 1, name: 'Shared', platform: 'openai', protocol: 'responses', models: [{ public_model: 'public-model', actual_model: 'actual-model', context_limit: 8192, max_output_tokens: 1024, rate_multiplier: '1', tiers: [{ from_tokens: 0, input_per_million: '2', output_per_million: '4', cache_read_per_million: '1', cache_write_per_million: '2' }] }] }] } })
async function setup() {
  const wrapper = mount(PoliciesView, { global: { stubs: { AppLayout: { template: '<div><slot /></div>' }, BaseDialog: { props: ['show'], template: '<div v-if="show" data-test="publish-dialog"><slot /><slot name="footer" /><button data-test="dismiss-dialog" @click="$emit(\'close\')">close</button></div>' }, TotpStepUpDialog: true } } })
  await flushPromises()
  return wrapper
}

describe('policy draft protection', () => {
  beforeEach(() => {
    state.workMode = reactive({ dirty: false, confirmLeave: vi.fn().mockResolvedValue(false) })
    state.get.mockReset().mockImplementation(async () => fixture())
    state.post.mockReset().mockResolvedValue({})
  })
  it('tracks terms and nested prices without marking the loaded policy dirty', async () => {
    const wrapper = await setup()
    expect(state.workMode.dirty).toBe(false)
    await wrapper.find('input').setValue('2100')
    expect(state.workMode.dirty).toBe(true)
    await wrapper.find('input').setValue('2000')
    expect(state.workMode.dirty).toBe(false)
    await wrapper.findAll('input').find(input => input.element.value === 'public-model')!.setValue('new-public-model')
    expect(state.workMode.dirty).toBe(true)
    wrapper.unmount()
    expect(state.workMode.dirty).toBe(false)
  })
  it('keeps the draft when refresh is cancelled and resets it after confirmed refresh', async () => {
    const wrapper = await setup()
    await wrapper.find('input').setValue('2100')
    await wrapper.find('header button').trigger('click'); await flushPromises()
    expect(state.workMode.confirmLeave).toHaveBeenCalledOnce()
    expect(state.get).toHaveBeenCalledOnce()
    expect(wrapper.find('input').element.value).toBe('2100')
    state.workMode.confirmLeave.mockResolvedValue(true)
    await wrapper.find('header button').trigger('click'); await flushPromises()
    expect(state.get).toHaveBeenCalledTimes(2)
    expect(wrapper.find('input').element.value).toBe('2000')
    expect(state.workMode.dirty).toBe(false)
    wrapper.unmount()
  })
  it('warns on browser unload only while the policy draft is dirty', async () => {
    const wrapper = await setup()
    const clean = new Event('beforeunload', { cancelable: true }); window.dispatchEvent(clean)
    expect(clean.defaultPrevented).toBe(false)
    await wrapper.find('input').setValue('2100')
    const dirty = new Event('beforeunload', { cancelable: true }); window.dispatchEvent(dirty)
    expect(dirty.defaultPrevented).toBe(true)
    wrapper.unmount()
    const removed = new Event('beforeunload', { cancelable: true }); window.dispatchEvent(removed)
    expect(removed.defaultPrevented).toBe(false)
  })
  it('locks publication, dismissal and editing while the request is in flight', async () => {
    let finish!: () => void
    state.post.mockImplementationOnce(() => new Promise<void>(resolve => { finish = resolve }))
    const wrapper = await setup()
    await wrapper.find('input').setValue('2100')
    await wrapper.find('form').trigger('submit')
    const dialog = wrapper.find('[data-test="publish-dialog"]')
    await dialog.findAll('button').find(button => button.text() === 'adminMarketplace.publish')!.trigger('click')
    await flushPromises()
    expect(wrapper.find('fieldset').attributes('disabled')).toBeDefined()
    expect(dialog.findAll('button').find(button => button.text() === 'common.cancel')!.attributes('disabled')).toBeDefined()
    await dialog.find('[data-test="dismiss-dialog"]').trigger('click')
    expect(wrapper.find('[data-test="publish-dialog"]').exists()).toBe(true)
    expect(state.leave?.()).toBe(false)
    finish(); await flushPromises()
    expect(wrapper.find('fieldset').attributes('disabled')).toBeUndefined()
    expect(wrapper.find('[data-test="publish-dialog"]').exists()).toBe(false)
    expect(state.workMode.dirty).toBe(false)
    wrapper.unmount()
  })
})
