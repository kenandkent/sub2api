import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { reactive } from 'vue'
import { webcrypto } from 'node:crypto'
if (!globalThis.crypto.subtle) Object.defineProperty(globalThis.crypto, 'subtle', { value: webcrypto.subtle, configurable: true })
import PayoutsView from '../PayoutsView.vue'

const state = vi.hoisted(() => ({ auth: { user: { id: 7 } as null | { id: number } }, payout: { id: 1, supplier_id: 7, status: 'processing', amount: '10.00', currency: 'USD' }, secret: 'private-destination', post: vi.fn(), get: vi.fn(), list: vi.fn(), run: vi.fn() }))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => state.auth }))
vi.mock('vue-i18n', async (importOriginal) => ({ ...(await importOriginal<typeof import('vue-i18n')>()), useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@/composables/useStepUp', () => ({ useStepUp: () => ({ run: state.run }), isStepUpCancelled: (e: any) => e?.code === 'STEP_UP_CANCELLED' }))
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
  beforeEach(() => { sessionStorage.clear(); state.auth = reactive({ user: { id: 7 } }); state.payout = { id: 1, supplier_id: 7, status: 'processing', amount: '10.00', currency: 'USD' }; state.get.mockReset().mockImplementation(async () => ({ ...state.payout })); state.list.mockReset().mockImplementation(async (path: string) => path === 'payouts' ? [state.payout] : [{ id: 2, supplier_id: 8, masked_label: '***', verified: false }]); state.post.mockReset().mockResolvedValue({ credential: state.secret }); state.run.mockReset().mockImplementation((fn: () => Promise<unknown>) => fn()) })
  it('prevents self confirmation and exposes no release or start action for processing', async () => { const wrapper = await setup(); await clickText(wrapper, 'adminMarketplace.details'); const buttons = wrapper.findAll('button'); const confirm = buttons.find(b => b.text() === 'adminMarketplace.confirm'); expect(confirm?.attributes('disabled')).toBeDefined(); expect(wrapper.text()).toContain('adminMarketplace.selfApproval'); expect(buttons.some(b => b.text() === 'adminMarketplace.start')).toBe(false); expect(buttons.some(b => b.text().includes('release'))).toBe(false) })
  it('clears revealed destination when its dialog closes', async () => { const wrapper = await setup(); await clickText(wrapper, 'adminMarketplace.reveal'); await wrapper.find('textarea').setValue('Verify beneficiary'); await clickText(wrapper, 'adminMarketplace.confirmAction'); await vi.waitFor(() => expect(wrapper.text()).toContain(state.secret)); await wrapper.findAll('.close-test').at(-1)!.trigger('click'); expect(wrapper.text()).not.toContain(state.secret) })
  it('requires matching amount and evidence to confirm payment', async () => { state.payout.supplier_id = 8; const wrapper = await setup(); await clickText(wrapper, 'adminMarketplace.details'); await clickText(wrapper, 'adminMarketplace.confirm'); const submit = wrapper.findAll('button').find(b => b.text() === 'adminMarketplace.confirmAction')!; expect(submit.attributes('disabled')).toBeDefined(); const inputs = wrapper.findAll('input'); await inputs[0].setValue('transfer-1'); await inputs[1].setValue('10.01'); await inputs[2].setValue('2026-10-28T10:00'); await wrapper.find('textarea').setValue('bank proof'); expect(submit.attributes('disabled')).toBeDefined(); await inputs[1].setValue('10.00'); expect(submit.attributes('disabled')).toBeUndefined() })
  it('checks current state and reuses the operation key after an uncertain approval', async () => {
    state.payout.supplier_id = 8; state.payout.status = 'requested'; state.post.mockRejectedValueOnce(new Error('network')).mockResolvedValueOnce({})
    const wrapper = await setup(); await clickText(wrapper, 'adminMarketplace.details'); await clickText(wrapper, 'adminMarketplace.approve'); await clickText(wrapper, 'adminMarketplace.confirmAction'); await vi.waitFor(() => expect(state.post).toHaveBeenCalledTimes(1))
    const retry = wrapper.findAll('button').find(b => b.text() === 'adminMarketplace.retryAction')!; expect(retry.attributes('disabled')).toBeDefined()
    await clickText(wrapper, 'adminMarketplace.checkStatus'); expect(retry.attributes('disabled')).toBeUndefined(); await retry.trigger('click'); await flushPromises()
    await vi.waitFor(() => expect(state.post).toHaveBeenCalledTimes(2)); expect(state.post.mock.calls[0][2]).toBe(state.post.mock.calls[1][2])
  })
  it('does not display a credential returned after closing the dialog', async () => {
    let resolve!: (value: unknown) => void; state.post.mockImplementationOnce(() => new Promise(r => { resolve = r }))
    const wrapper = await setup(); await clickText(wrapper, 'adminMarketplace.reveal'); await wrapper.find('textarea').setValue('Verify beneficiary'); await wrapper.findAll('button').find(b => b.text() === 'adminMarketplace.confirmAction')!.trigger('click'); await vi.waitFor(() => expect(state.run).toHaveBeenCalled()); await wrapper.findAll('.close-test').at(-1)!.trigger('click'); resolve({ credential: state.secret }); await flushPromises(); expect(wrapper.text()).not.toContain(state.secret)
  })

  it('does not dispatch a reveal after its step-up dialog is closed', async () => {
    let resume!: () => void
    state.run.mockImplementationOnce(async (fn: () => Promise<unknown>) => { await new Promise<void>(resolve => { resume = resolve }); return fn() })
    const wrapper = await setup(); await clickText(wrapper, 'adminMarketplace.reveal'); await wrapper.find('textarea').setValue('Verify beneficiary')
    await wrapper.findAll('button').find(b => b.text() === 'adminMarketplace.confirmAction')!.trigger('click'); await vi.waitFor(() => expect(state.run).toHaveBeenCalled())
    await wrapper.findAll('.close-test').at(-1)!.trigger('click'); resume(); await flushPromises()
    expect(state.post).not.toHaveBeenCalled()
    expect(wrapper.text()).not.toContain(state.secret)
  })
  it('does not show a pending reveal after logout and a new login', async () => {
    let resolve!: (value: unknown) => void; state.post.mockImplementationOnce(() => new Promise(r => { resolve = r }))
    const wrapper = await setup(); await clickText(wrapper, 'adminMarketplace.reveal'); await wrapper.find('textarea').setValue('Verify beneficiary')
    await wrapper.findAll('button').find(b => b.text() === 'adminMarketplace.confirmAction')!.trigger('click'); await vi.waitFor(() => expect(state.run).toHaveBeenCalled())
    state.auth.user = null; await flushPromises(); state.auth.user = { id: 8 }; await flushPromises()
    await clickText(wrapper, 'adminMarketplace.reveal'); resolve({ credential: state.secret }); await flushPromises()
    expect(wrapper.text()).not.toContain(state.secret)
  })
  it('does not show a reveal after switching directly to another user', async () => {
    let resolve!: (value: unknown) => void; state.post.mockImplementationOnce(() => new Promise(r => { resolve = r }))
    const wrapper = await setup(); await clickText(wrapper, 'adminMarketplace.reveal'); await wrapper.find('textarea').setValue('Verify beneficiary')
    await wrapper.findAll('button').find(b => b.text() === 'adminMarketplace.confirmAction')!.trigger('click'); await vi.waitFor(() => expect(state.run).toHaveBeenCalled())
    state.auth.user = { id: 8 }; await flushPromises(); resolve({ credential: state.secret }); await flushPromises()
    expect(wrapper.text()).not.toContain(state.secret)
  })

  it('does not dispatch an old reveal after opening a different destination action', async () => {
    let resume!: () => void
    state.run.mockImplementationOnce(async (fn: () => Promise<unknown>) => { await new Promise<void>(resolve => { resume = resolve }); return fn() })
    const wrapper = await setup(); await clickText(wrapper, 'adminMarketplace.reveal'); await wrapper.find('textarea').setValue('Verify beneficiary')
    await wrapper.findAll('button').find(b => b.text() === 'adminMarketplace.confirmAction')!.trigger('click'); await vi.waitFor(() => expect(state.run).toHaveBeenCalled())
    await wrapper.findAll('.close-test').at(-1)!.trigger('click'); await clickText(wrapper, 'adminMarketplace.verify'); resume(); await flushPromises()
    expect(state.post).not.toHaveBeenCalled()
  })

  it('does not dispatch a payout action after changing its dialog while step-up is pending', async () => {
    let resume!: () => void
    state.payout.supplier_id = 8; state.payout.status = 'requested'
    state.run.mockImplementationOnce(async (fn: () => Promise<unknown>) => { await new Promise<void>(resolve => { resume = resolve }); return fn() })
    const wrapper = await setup(); await clickText(wrapper, 'adminMarketplace.details'); await clickText(wrapper, 'adminMarketplace.approve')
    await wrapper.findAll('button').find(b => b.text() === 'adminMarketplace.confirmAction')!.trigger('click'); await vi.waitFor(() => expect(state.run).toHaveBeenCalled())
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

  it('allows correction after cancelling a step-up challenge before mutation', async () => {
    state.payout.supplier_id = 8; state.payout.status = 'processing'; state.run.mockRejectedValueOnce({ code: 'STEP_UP_CANCELLED' })
    const wrapper = await setup(); await clickText(wrapper, 'adminMarketplace.details'); await clickText(wrapper, 'adminMarketplace.confirm')
    const inputs = wrapper.findAll('input'); await inputs[0].setValue('wrong'); await inputs[1].setValue('10.00'); await inputs[2].setValue('2026-10-28T10:00'); await wrapper.find('textarea').setValue('proof')
    await clickText(wrapper, 'adminMarketplace.confirmAction'); await vi.waitFor(() => expect(state.run).toHaveBeenCalledTimes(1)); await flushPromises()
    expect(inputs[0].attributes('disabled')).toBeUndefined()
    await inputs[0].setValue('corrected')
    await clickText(wrapper, 'adminMarketplace.confirmAction'); await vi.waitFor(() => expect(state.post).toHaveBeenCalledTimes(1))
    expect(state.post).toHaveBeenCalledWith('payouts/1/confirm', expect.objectContaining({ external_reference: 'corrected' }), expect.any(String))
  })

  it('retains an earlier uncertain payout operation when a later step-up is cancelled', async () => {
    state.payout.supplier_id = 8; state.payout.status = 'requested'; state.post.mockRejectedValueOnce(new Error('network'))
    const wrapper = await setup(); await clickText(wrapper, 'adminMarketplace.details'); await clickText(wrapper, 'adminMarketplace.approve'); await clickText(wrapper, 'adminMarketplace.confirmAction')
    await vi.waitFor(() => expect(state.post).toHaveBeenCalledTimes(1)); const firstKey = state.post.mock.calls[0][2]; await clickText(wrapper, 'adminMarketplace.checkStatus'); state.run.mockRejectedValueOnce({ code: 'STEP_UP_CANCELLED' }); await clickText(wrapper, 'adminMarketplace.retryAction'); await vi.waitFor(() => expect(state.run).toHaveBeenCalledTimes(2)); await flushPromises()
    expect(wrapper.text()).toContain('adminMarketplace.retrySame'); await clickText(wrapper, 'adminMarketplace.checkStatus'); state.post.mockResolvedValueOnce({}); await clickText(wrapper, 'adminMarketplace.retryAction')
    await vi.waitFor(() => expect(state.post).toHaveBeenCalledTimes(2)); expect(state.post.mock.calls.at(-1)[2]).toBe(firstKey)
  })

  it('restores an uncertain payout operation and requires the same evidence after reload', async () => {
    state.payout.supplier_id = 8; state.payout.status = 'processing'; state.post.mockRejectedValueOnce(new Error('network')).mockResolvedValueOnce({})
    const first = await setup(); await clickText(first, 'adminMarketplace.details'); await clickText(first, 'adminMarketplace.confirm')
    let inputs = first.findAll('input'); await inputs[0].setValue('ref-secret'); await inputs[1].setValue('10.00'); await inputs[2].setValue('2026-10-28T10:00'); await first.find('textarea').setValue('proof-secret'); await clickText(first, 'adminMarketplace.confirmAction')
    await vi.waitFor(() => expect(state.post).toHaveBeenCalledTimes(1)); const key = state.post.mock.calls[0][2]; expect(JSON.stringify(sessionStorage)).toContain(key); expect(JSON.stringify(sessionStorage)).not.toContain('proof-secret'); expect(JSON.stringify(sessionStorage)).not.toContain('ref-secret'); expect(JSON.stringify(sessionStorage)).not.toContain('10.00'); first.unmount()
    const second = await setup(); await clickText(second, 'adminMarketplace.details'); await clickText(second, 'adminMarketplace.confirm'); await clickText(second, 'adminMarketplace.checkStatus')
    inputs = second.findAll('input'); await inputs[0].setValue('ref-secret'); await inputs[1].setValue('10.00'); await inputs[2].setValue('2026-10-28T10:00'); await second.find('textarea').setValue('wrong proof'); await clickText(second, 'adminMarketplace.retryAction'); await vi.waitFor(() => expect(second.text()).toContain('adminMarketplace.retryPayloadMismatch')); expect(state.post).toHaveBeenCalledTimes(1)
    await second.find('textarea').setValue('proof-secret'); await clickText(second, 'adminMarketplace.retryAction'); await vi.waitFor(() => expect(state.post).toHaveBeenCalledTimes(2)); expect(state.post.mock.calls[1][2]).toBe(key); second.unmount()
  })
  it('restores an uncertain destination verification without storing evidence', async () => {
    state.post.mockRejectedValueOnce(new Error('network')).mockResolvedValueOnce({})
    const first = await setup(); await clickText(first, 'adminMarketplace.verify'); await first.find('textarea').setValue('private-proof'); await clickText(first, 'adminMarketplace.confirmAction')
    await vi.waitFor(() => expect(state.post).toHaveBeenCalledTimes(1)); const key = state.post.mock.calls[0][2]; expect(JSON.stringify(sessionStorage)).toContain(key); expect(JSON.stringify(sessionStorage)).not.toContain('private-proof'); first.unmount()
    const second = await setup(); await clickText(second, 'adminMarketplace.verify'); await second.find('textarea').setValue('private-proof'); await clickText(second, 'adminMarketplace.retryAction')
    await vi.waitFor(() => expect(state.post).toHaveBeenCalledTimes(2)); expect(state.post.mock.calls[1][2]).toBe(key); second.unmount()
  })

  it('does not show a previous user’s delayed list after identity changes', async () => {
    let resolveOld!: (value: unknown) => void
    state.list.mockImplementation((path: string) => path === 'payouts' && state.auth.user?.id === 7 ? new Promise(r => { resolveOld = r }) : Promise.resolve(path === 'payouts' ? [{ ...state.payout, id: 99 }] : []))
    const wrapper = mount(PayoutsView, { global: { stubs } }); await flushPromises(); state.auth.user = { id: 8 }; await flushPromises(); resolveOld([{ ...state.payout, id: 1 }]); await flushPromises()
    expect(wrapper.find('[data-row="1"]').exists()).toBe(false); expect(wrapper.find('[data-row="99"]').exists()).toBe(true); wrapper.unmount()
  })

  it('retains the original local payment time when reopening an uncertain action', async () => {
    const oldTZ = process.env.TZ; process.env.TZ = 'Asia/Shanghai'
    try { state.payout.supplier_id = 8; state.payout.status = 'processing'; state.post.mockRejectedValueOnce(new Error('network')); const w = await setup(); await clickText(w, 'adminMarketplace.details'); await clickText(w, 'adminMarketplace.confirm'); const inputs = w.findAll('input'); await inputs[0].setValue('ref'); await inputs[1].setValue('10.00'); await inputs[2].setValue('2026-10-28T10:00'); await w.find('textarea').setValue('proof'); await clickText(w, 'adminMarketplace.confirmAction'); await vi.waitFor(() => expect(state.post).toHaveBeenCalledTimes(1)); await w.findAll('.close-test').at(-1)!.trigger('click'); await clickText(w, 'adminMarketplace.confirm'); expect((w.findAll('input')[2].element as HTMLInputElement).value).toBe('2026-10-28T10:00'); w.unmount() } finally { process.env.TZ = oldTZ }
  })

})
