import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { reactive } from 'vue'
import PayoutsView from '../PayoutsView.vue'

const state = vi.hoisted(() => ({ auth: { user: { id: 7 } as null | { id: number } }, payout: { id: 1, supplier_id: 7, status: 'processing', amount: '10.00', currency: 'USD' }, secret: 'private-destination', post: vi.fn(), get: vi.fn(), list: vi.fn(), run: vi.fn() }))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => state.auth }))
vi.mock('vue-i18n', async (importOriginal) => ({ ...(await importOriginal<typeof import('vue-i18n')>()), useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@/composables/useStepUp', () => ({ useStepUp: () => ({ run: state.run }), isStepUpCancelled: () => false }))
vi.mock('@/api/admin/marketplace', () => ({ get: state.get, list: state.list, post: state.post, items: (x: unknown) => x }))
const stubs = {
  AppLayout: { template: '<div><slot /></div>' },
  DataTable: { props: ['data'], template: '<div><div v-for="row in data" :key="row.id" :data-row="row.id"><slot name="cell-actions" :row="row" /></div></div>' },
  Pagination: { template: `<button class="next-page" @click="$emit('update:page', 2)">next</button>` },
  BaseDialog: { props: ['show', 'title'], template: '<div v-if="show"><h2>{{title}}</h2><slot /><slot name="footer" /><button class="close-test" @click="$emit(\'close\')">close</button></div>' },
  TotpStepUpDialog: true
}
async function setup() { const wrapper = mount(PayoutsView, { global: { stubs } }); await flushPromises(); return wrapper }
async function clickText(wrapper: ReturnType<typeof mount>, text: string) { const button = wrapper.findAll('button').find(b => b.text() === text); expect(button).toBeTruthy(); await button!.trigger('click'); await flushPromises() }

describe('admin payout safeguards', () => {
  beforeEach(() => { state.auth = reactive({ user: { id: 7 } }); state.payout = { id: 1, supplier_id: 7, status: 'processing', amount: '10.00', currency: 'USD' }; state.get.mockReset().mockImplementation(async () => ({ ...state.payout })); state.list.mockReset().mockImplementation(async (path: string) => path === 'payouts' ? [state.payout] : [{ id: 2, supplier_id: 8, masked_label: '***', verified: false }]); state.post.mockReset().mockResolvedValue({ credential: state.secret }); state.run.mockReset().mockImplementation((fn: () => Promise<unknown>) => fn()) })
  it('prevents self confirmation and exposes no release or start action for processing', async () => { const wrapper = await setup(); await clickText(wrapper, 'adminMarketplace.details'); const buttons = wrapper.findAll('button'); const confirm = buttons.find(b => b.text() === 'adminMarketplace.confirm'); expect(confirm?.attributes('disabled')).toBeDefined(); expect(wrapper.text()).toContain('adminMarketplace.selfApproval'); expect(buttons.some(b => b.text() === 'adminMarketplace.start')).toBe(false); expect(buttons.some(b => b.text().includes('release'))).toBe(false) })
  it('clears revealed destination when its dialog closes', async () => { const wrapper = await setup(); await clickText(wrapper, 'adminMarketplace.reveal'); await wrapper.find('textarea').setValue('Verify beneficiary'); await clickText(wrapper, 'adminMarketplace.confirmAction'); expect(wrapper.text()).toContain(state.secret); await wrapper.findAll('.close-test').at(-1)!.trigger('click'); expect(wrapper.text()).not.toContain(state.secret) })
  it('requires matching amount and evidence to confirm payment', async () => { state.payout.supplier_id = 8; const wrapper = await setup(); await clickText(wrapper, 'adminMarketplace.details'); await clickText(wrapper, 'adminMarketplace.confirm'); const submit = wrapper.findAll('button').find(b => b.text() === 'adminMarketplace.confirmAction')!; expect(submit.attributes('disabled')).toBeDefined(); const inputs = wrapper.findAll('input'); await inputs[0].setValue('transfer-1'); await inputs[1].setValue('10.01'); await inputs[2].setValue('2026-10-28T10:00'); await wrapper.find('textarea').setValue('bank proof'); expect(submit.attributes('disabled')).toBeDefined(); await inputs[1].setValue('10.00'); expect(submit.attributes('disabled')).toBeUndefined() })
  it('checks current state and reuses the operation key after an uncertain approval', async () => {
    state.payout.supplier_id = 8; state.payout.status = 'requested'; state.post.mockRejectedValueOnce(new Error('network')).mockResolvedValueOnce({})
    const wrapper = await setup(); await clickText(wrapper, 'adminMarketplace.details'); await clickText(wrapper, 'adminMarketplace.approve'); await clickText(wrapper, 'adminMarketplace.confirmAction')
    const retry = wrapper.findAll('button').find(b => b.text() === 'adminMarketplace.retryAction')!; expect(retry.attributes('disabled')).toBeDefined()
    await clickText(wrapper, 'adminMarketplace.checkStatus'); expect(retry.attributes('disabled')).toBeUndefined(); await retry.trigger('click'); await flushPromises()
    expect(state.post.mock.calls[0][2]).toBe(state.post.mock.calls[1][2])
  })
  it('does not display a credential returned after closing the dialog', async () => {
    let resolve!: (value: unknown) => void; state.post.mockImplementationOnce(() => new Promise(r => { resolve = r }))
    const wrapper = await setup(); await clickText(wrapper, 'adminMarketplace.reveal'); await wrapper.find('textarea').setValue('Verify beneficiary'); await wrapper.findAll('button').find(b => b.text() === 'adminMarketplace.confirmAction')!.trigger('click'); await wrapper.findAll('.close-test').at(-1)!.trigger('click'); resolve({ credential: state.secret }); await flushPromises(); expect(wrapper.text()).not.toContain(state.secret)
  })

  it('does not dispatch a reveal after its step-up dialog is closed', async () => {
    let resume!: () => void
    state.run.mockImplementationOnce(async (fn: () => Promise<unknown>) => { await new Promise<void>(resolve => { resume = resolve }); return fn() })
    const wrapper = await setup(); await clickText(wrapper, 'adminMarketplace.reveal'); await wrapper.find('textarea').setValue('Verify beneficiary')
    await wrapper.findAll('button').find(b => b.text() === 'adminMarketplace.confirmAction')!.trigger('click')
    await wrapper.findAll('.close-test').at(-1)!.trigger('click'); resume(); await flushPromises()
    expect(state.post).not.toHaveBeenCalled()
    expect(wrapper.text()).not.toContain(state.secret)
  })
  it('does not show a pending reveal after logout and a new login', async () => {
    let resolve!: (value: unknown) => void; state.post.mockImplementationOnce(() => new Promise(r => { resolve = r }))
    const wrapper = await setup(); await clickText(wrapper, 'adminMarketplace.reveal'); await wrapper.find('textarea').setValue('Verify beneficiary')
    await wrapper.findAll('button').find(b => b.text() === 'adminMarketplace.confirmAction')!.trigger('click')
    state.auth.user = null; await flushPromises(); state.auth.user = { id: 8 }; await flushPromises()
    await clickText(wrapper, 'adminMarketplace.reveal'); resolve({ credential: state.secret }); await flushPromises()
    expect(wrapper.text()).not.toContain(state.secret)
  })
  it('does not show a reveal after switching directly to another user', async () => {
    let resolve!: (value: unknown) => void; state.post.mockImplementationOnce(() => new Promise(r => { resolve = r }))
    const wrapper = await setup(); await clickText(wrapper, 'adminMarketplace.reveal'); await wrapper.find('textarea').setValue('Verify beneficiary')
    await wrapper.findAll('button').find(b => b.text() === 'adminMarketplace.confirmAction')!.trigger('click')
    state.auth.user = { id: 8 }; await flushPromises(); resolve({ credential: state.secret }); await flushPromises()
    expect(wrapper.text()).not.toContain(state.secret)
  })

  it('does not dispatch an old reveal after opening a different destination action', async () => {
    let resume!: () => void
    state.run.mockImplementationOnce(async (fn: () => Promise<unknown>) => { await new Promise<void>(resolve => { resume = resolve }); return fn() })
    const wrapper = await setup(); await clickText(wrapper, 'adminMarketplace.reveal'); await wrapper.find('textarea').setValue('Verify beneficiary')
    await wrapper.findAll('button').find(b => b.text() === 'adminMarketplace.confirmAction')!.trigger('click')
    await wrapper.findAll('.close-test').at(-1)!.trigger('click'); await clickText(wrapper, 'adminMarketplace.verify'); resume(); await flushPromises()
    expect(state.post).not.toHaveBeenCalled()
  })

  it('does not dispatch a payout action after changing its dialog while step-up is pending', async () => {
    let resume!: () => void
    state.payout.supplier_id = 8; state.payout.status = 'requested'
    state.run.mockImplementationOnce(async (fn: () => Promise<unknown>) => { await new Promise<void>(resolve => { resume = resolve }); return fn() })
    const wrapper = await setup(); await clickText(wrapper, 'adminMarketplace.details'); await clickText(wrapper, 'adminMarketplace.approve')
    await wrapper.findAll('button').find(b => b.text() === 'adminMarketplace.confirmAction')!.trigger('click')
    await wrapper.findAll('.close-test').at(-1)!.trigger('click'); await clickText(wrapper, 'adminMarketplace.reject'); resume(); await flushPromises()
    expect(state.post).not.toHaveBeenCalled()
  })

  it('paginates array responses returned by the admin list endpoints', async () => {
    state.list.mockImplementation(async (path: string) => path === 'payouts' ? Array.from({ length: 21 }, (_, i) => ({ ...state.payout, id: i + 1 })) : [])
    const wrapper = await setup()
    expect(wrapper.find('[data-row="21"]').exists()).toBe(false)
    await wrapper.find('.next-page').trigger('click'); await flushPromises()
    expect(wrapper.find('[data-row="1"]').exists()).toBe(false)
    expect(wrapper.find('[data-row="21"]').exists()).toBe(true)
  })

})
