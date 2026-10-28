import { webcrypto } from 'node:crypto'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import FundingAdminToolsDialog from '../FundingAdminToolsDialog.vue'

const mocks = vi.hoisted(() => ({ auth: null as any, get: vi.fn(), post: vi.fn(), run: vi.fn(), cancelled: { code: 'cancelled' } }))
vi.mock('@/stores/auth', async () => { const { reactive } = await import('vue'); mocks.auth = reactive({ user: { id: 42 } }); return { useAuthStore: () => mocks.auth } })
vi.mock('@/api/admin/marketplace', () => ({ get: mocks.get, post: mocks.post }))
vi.mock('@/composables/useStepUp', () => ({ useStepUp: () => ({ run: mocks.run }), isStepUpCancelled: (e: unknown) => e === mocks.cancelled }))
vi.mock('vue-i18n', async (original) => ({ ...(await original<typeof import('vue-i18n')>()), useI18n: () => ({ t: (s: string) => s }) }))
const stubs = { BaseDialog: { props: ['show'], template: '<div v-if="show"><slot/><slot name="footer"/></div>' }, TotpStepUpDialog: true }
const savedRecords = () => Object.values(localStorage).map(raw => JSON.parse(raw)) as Array<{ key: string; phase: string }>
const pendingCount = () => savedRecords().filter(item => item.phase === 'uncertain').length
const make = () => mount(FundingAdminToolsDialog, { props: { show: true }, global: { stubs } })
const settle = async () => { for (let i = 0; i < 5; i++) { await new Promise(resolve => setTimeout(resolve, 0)); await flushPromises() } }
async function fillReclass(w: ReturnType<typeof make>, amount = '1.25') { await w.get('[data-test=reclass-user]').setValue('10'); await w.get('[data-test=reclass-order]').setValue('20'); await w.get('[data-test=reclass-amount]').setValue(amount); await w.get('[data-test=reclass-evidence]').setValue('evidence secret'); await w.get('[data-test=reclass-confirm]').setValue(true) }

beforeEach(() => { sessionStorage.clear(); localStorage.clear(); const chains = new Map<string, Promise<unknown>>(); Object.defineProperty(navigator, 'locks', { configurable: true, value: { request: (name: string, _options: unknown, fn: () => Promise<unknown>) => { const next = (chains.get(name) || Promise.resolve()).then(fn); chains.set(name, next.catch(() => undefined)); return next } } }); vi.clearAllMocks(); mocks.auth.user = { id: 42 }; vi.stubGlobal('crypto', webcrypto); mocks.get.mockResolvedValue([]); mocks.run.mockImplementation((fn: () => Promise<unknown>) => fn()) })
describe('audited funding tools', () => {
  it('requires a current unspent-source confirmation before submitting', async () => {
    const w = make(); await fillReclass(w); await w.get('[data-test=reclass-confirm]').setValue(false); expect(w.get('[data-test=reclass-submit]').attributes('disabled')).toBeDefined(); expect(mocks.post).not.toHaveBeenCalled()
    await w.get('[data-test=reclass-confirm]').setValue(true); mocks.post.mockResolvedValue({}); await w.get('[data-test=reclass-submit]').trigger('click'); await settle(); expect(mocks.post.mock.calls[0][1].remaining_provenance_confirmed).toBe(true); w.unmount()
  })
  it('recovers an unknown reclassification only with the original key and exact re-entered body', async () => {
    mocks.post.mockRejectedValueOnce(new Error('unknown')).mockResolvedValueOnce({ user_id: 10, order_id: 20, amount: '1.25', cash_amount: '1', bonus_amount: '.25' })
    const first = make(); await fillReclass(first); await first.get('[data-test=reclass-submit]').trigger('click'); await settle()
    const firstCall = mocks.post.mock.calls[0]; expect(firstCall).toBeTruthy()
    const saved = JSON.stringify(localStorage); expect(saved).toContain(firstCall[2]); expect(saved).not.toContain('evidence secret'); expect(saved).not.toContain('1.25')
    first.unmount(); const second = make(); await settle(); await fillReclass(second, '2.25'); await second.get('[data-test=reclass-retry]').trigger('click'); await settle(); expect(mocks.post).toHaveBeenCalledTimes(1)
    await second.get('[data-test=reclass-amount]').setValue('1.25'); await second.get('[data-test=reclass-retry]').trigger('click'); await settle()
    expect(mocks.post.mock.calls[1]).toEqual(firstCall); expect(savedRecords().find(item => item.key === firstCall[2])?.phase).toBe('succeeded'); second.unmount()
  })
  it('clears a cancelled first step-up but retains a prior uncertain operation on cancelled retry', async () => {
    mocks.run.mockRejectedValueOnce(mocks.cancelled); const w = make(); await fillReclass(w); await w.get('[data-test=reclass-submit]').trigger('click'); await settle(); expect(pendingCount()).toBe(0)
    mocks.run.mockImplementation((fn: () => Promise<unknown>) => fn()); mocks.post.mockRejectedValueOnce(new Error('unknown')); await w.get('[data-test=reclass-submit]').trigger('click'); await settle(); const saved = JSON.stringify(localStorage)
    mocks.run.mockRejectedValueOnce(mocks.cancelled); await w.get('[data-test=reclass-retry]').trigger('click'); await settle(); expect(JSON.stringify(localStorage)).toBe(saved); w.unmount()
  })
  it('requires explicit action for each vault batch and gives each a new operation key', async () => {
    mocks.post.mockImplementation(async (_path, body) => ({ operation_id: body.operation_id, processed: 3, remaining: 2, completed: false, target_key_id: 'key-v2' }))
    const w = make(); await w.get('[data-test=tab-vault]').trigger('click'); await w.get('[data-test=vault-limit]').setValue('3'); await w.get('[data-test=vault-reason]').setValue('rotation audit'); await w.get('[data-test=vault-submit]').trigger('click'); await settle()
    expect(mocks.post).toHaveBeenCalledTimes(1); expect(w.text()).toContain('2'); const first = mocks.post.mock.calls[0]
    await w.get('[data-test=vault-next]').trigger('click'); await settle(); expect(mocks.post).toHaveBeenCalledTimes(2); const second = mocks.post.mock.calls[1]; expect(second[2]).not.toBe(first[2]); expect(second[1].operation_id).toBe(second[2]); w.unmount()
  })
  it('does not allow risk clearing while debt remains and sends the selected version', async () => {
    mocks.get.mockResolvedValue([{ user_id: 8, version: 4, reason: 'chargeback', debt: '2.00' }, { user_id: 9, version: 7, reason: 'review', debt: '0.00000000' }]); mocks.post.mockResolvedValue({ user_id: 9, version: 7 })
    const w = make(); await w.get('[data-test=tab-risk]').trigger('click'); await settle(); expect(w.get('[data-test=risk-clear-8]').attributes('disabled')).toBeDefined(); await w.get('[data-test=risk-clear-9]').trigger('click'); await w.get('[data-test=risk-evidence]').setValue('reviewed'); await w.get('[data-test=risk-submit]').trigger('click'); await settle()
    expect(mocks.post.mock.calls[0][0]).toBe('funding-risks/9/clear'); expect(mocks.post.mock.calls[0][1]).toEqual({ expected_version: 7, evidence: 'reviewed' }); w.unmount()
  })
  it('persists only recovery metadata before dispatch and fails closed if storage is unavailable', async () => {
    mocks.post.mockImplementationOnce(async (_path, body, key) => { const metadata = Object.values(localStorage).join(' '); expect(metadata).toContain(key); expect(metadata).not.toContain(body.amount); expect(metadata).not.toContain(body.evidence); return {} })
    const w = make(); await fillReclass(w); await w.get('[data-test=reclass-submit]').trigger('click'); await settle(); expect(mocks.post).toHaveBeenCalledTimes(1)
    const denied = vi.spyOn(Storage.prototype, 'setItem').mockImplementationOnce(() => { throw new Error('storage disabled') }); await fillReclass(w); await w.get('[data-test=reclass-submit]').trigger('click'); await settle(); expect(mocks.post).toHaveBeenCalledTimes(1); expect(w.text()).toContain('adminFundingTools.storageBlocked'); denied.mockRestore(); w.unmount()
  })
  it('restores an uncertain risk decision after reload with original version and evidence', async () => {
    mocks.get.mockResolvedValue([{ user_id: 9, version: 7, reason: 'review', debt: '0' }]); mocks.post.mockRejectedValueOnce(new Error('unknown')).mockResolvedValueOnce({})
    const first = make(); await first.get('[data-test=tab-risk]').trigger('click'); await settle(); await first.get('[data-test=risk-clear-9]').trigger('click'); await first.get('[data-test=risk-evidence]').setValue('sensitive review'); await first.get('[data-test=risk-submit]').trigger('click'); await settle(); const call = mocks.post.mock.calls[0]; expect(JSON.stringify(localStorage)).not.toContain('sensitive review'); first.unmount()
    const second = make(); await settle(); await second.get('[data-test=risk-user]').setValue('9'); await second.get('[data-test=risk-version]').setValue('8'); await second.get('[data-test=risk-evidence]').setValue('sensitive review'); await second.get('[data-test=risk-retry]').trigger('click'); await settle(); expect(mocks.post).toHaveBeenCalledTimes(1)
    await second.get('[data-test=risk-version]').setValue('7'); await second.get('[data-test=risk-retry]').trigger('click'); await settle(); expect(mocks.post.mock.calls[1]).toEqual(call); second.unmount()
  })
  it('requires the same reason and limit to retry an uncertain vault batch after reload', async () => {
    mocks.post.mockRejectedValueOnce(new Error('network')).mockImplementationOnce(async (_p, b) => ({ operation_id: b.operation_id, processed: 1, remaining: 0, completed: true, target_key_id: 'v2' }))
    const first = make(); await first.get('[data-test=tab-vault]').trigger('click'); await first.get('[data-test=vault-limit]').setValue('2'); await first.get('[data-test=vault-reason]').setValue('evidence secret'); await first.get('[data-test=vault-submit]').trigger('click'); await settle(); const call = mocks.post.mock.calls[0]; expect(JSON.stringify(localStorage)).not.toContain('evidence secret'); first.unmount()
    const second = make(); await settle(); await second.get('[data-test=vault-limit]').setValue('3'); await second.get('[data-test=vault-reason]').setValue('evidence secret'); await second.get('[data-test=vault-retry]').trigger('click'); await settle(); expect(mocks.post).toHaveBeenCalledTimes(1)
    await second.get('[data-test=vault-limit]').setValue('2'); await second.get('[data-test=vault-retry]').trigger('click'); await settle(); expect(mocks.post.mock.calls[1]).toEqual(call); expect(second.text()).toContain('v2'); second.unmount()
  })
  it('does not overwrite another mounted dialog’s pending operation', async () => {
    mocks.post.mockRejectedValue(new Error('unknown')); const first = make(), second = make(); await fillReclass(first); await fillReclass(second, '3.00'); await first.get('[data-test=reclass-submit]').trigger('click'); await settle(); const saved = JSON.stringify(localStorage)
    await second.get('[data-test=reclass-submit]').trigger('click'); await settle(); expect(mocks.post).toHaveBeenCalledTimes(1); expect(JSON.stringify(localStorage)).toBe(saved); expect(second.find('[data-test=reclass-retry]').exists()).toBe(true); first.unmount(); second.unmount()
  })
  it('requires step-up for risk reads and discards a late list after identity changes', async () => {
    let finish!: (value: unknown) => void; mocks.get.mockImplementationOnce(() => new Promise(resolve => { finish = resolve })); const w = make(); await w.get('[data-test=tab-risk]').trigger('click'); await settle(); expect(mocks.run).toHaveBeenCalledOnce(); expect(mocks.get).toHaveBeenCalledWith('funding-risks')
    mocks.auth.user = { id: 84 }; await settle(); finish([{ user_id: 9, version: 7, reason: 'private risk note', debt: '0' }]); await settle(); expect(w.text()).not.toContain('private risk note'); w.unmount()
  })
  it('clears only proven no-effect errors and allows correcting a rejected order', async () => {
    mocks.post.mockRejectedValueOnce({ status: 400, reason: 'FUNDING_OPERATION_REJECTED' }).mockResolvedValueOnce({}); const w = make(); await fillReclass(w); await w.get('[data-test=reclass-submit]').trigger('click'); await settle(); expect(pendingCount()).toBe(0); expect(w.find('[data-test=reclass-submit]').exists()).toBe(true)
    await w.get('[data-test=reclass-order]').setValue('21'); await w.get('[data-test=reclass-submit]').trigger('click'); await settle(); expect(mocks.post).toHaveBeenCalledTimes(2); expect(mocks.post.mock.calls[1][2]).not.toBe(mocks.post.mock.calls[0][2]); w.unmount()
  })
  it('clears a proven stale risk version but retains generic 400 and fingerprint conflicts', async () => {
    mocks.get.mockResolvedValue([{ user_id: 9, version: 7, reason: 'review', debt: '0' }]); mocks.post.mockRejectedValueOnce({status:409,reason:'FUNDING_RISK_STALE'}); const w=make(); await w.get('[data-test=tab-risk]').trigger('click'); await settle(); await w.get('[data-test=risk-clear-9]').trigger('click'); await w.get('[data-test=risk-evidence]').setValue('reviewed'); await w.get('[data-test=risk-submit]').trigger('click'); await settle(); expect(pendingCount()).toBe(0); expect(w.find('[data-test=risk-submit]').exists()).toBe(false)
    await w.get('[data-test=tab-reclass]').trigger('click'); await fillReclass(w); mocks.post.mockRejectedValueOnce({status:400}); await w.get('[data-test=reclass-submit]').trigger('click'); await settle(); const saved=JSON.stringify(localStorage); expect(pendingCount()).toBe(1)
    mocks.post.mockRejectedValueOnce({status:409,reason:'FUNDING_OPERATION_CONFLICT'}); await w.get('[data-test=reclass-retry]').trigger('click'); await settle(); expect(JSON.stringify(localStorage)).toBe(saved); w.unmount()
  })
  it('stores multiple per-actor operations durably and recovers them independently', async () => {
    mocks.post.mockRejectedValue(new Error('network')); const first=make(); await fillReclass(first); await first.get('[data-test=reclass-submit]').trigger('click'); await settle(); const firstKey=mocks.post.mock.calls[0][2]
    await first.get('[data-test=tab-vault]').trigger('click'); await first.get('[data-test=vault-limit]').setValue('2'); await first.get('[data-test=vault-reason]').setValue('rotation reason'); await first.get('[data-test=vault-submit]').trigger('click'); await settle(); const vaultKey=mocks.post.mock.calls[1][2]; expect(localStorage.length).toBe(2); first.unmount(); sessionStorage.clear()
    const second=make(); await settle(); expect(second.text()).toContain(firstKey); expect(second.text()).toContain(vaultKey); expect(second.findAll('[data-test=pending-resume]')).toHaveLength(2); second.unmount()
  })
  it('rejects an oversized serialized vault body before creating any operation', async () => {
    const w=make(); await w.get('[data-test=tab-vault]').trigger('click'); await w.get('[data-test=vault-reason]').setValue('"'.repeat(4096)); await w.get('[data-test=vault-submit]').trigger('click'); await settle(); expect(mocks.post).not.toHaveBeenCalled(); expect(pendingCount()).toBe(0); expect(w.text()).toContain('adminFundingTools.bodyTooLarge'); w.unmount()
  })
  it('serializes simultaneous submissions for the same target and preserves the original key', async () => {
    let finish!: (value: unknown) => void; mocks.post.mockImplementationOnce(() => new Promise(resolve => { finish = resolve })).mockResolvedValueOnce({})
    const first=make(),second=make(); await fillReclass(first); await fillReclass(second); await Promise.all([first.get('[data-test=reclass-submit]').trigger('click'), second.get('[data-test=reclass-submit]').trigger('click')]); await settle()
    expect(mocks.post).toHaveBeenCalledTimes(1); expect(pendingCount()).toBe(1); const call=mocks.post.mock.calls[0]; first.unmount(); finish({}); await settle()
    expect(second.find('[data-test=reclass-retry]').exists()).toBe(true); await second.get('[data-test=reclass-retry]').trigger('click'); await settle(); expect(mocks.post.mock.calls[1]).toEqual(call); second.unmount()
  })
  it('migrates prior session-only recovery metadata to durable storage without losing the key', async () => {
    mocks.post.mockRejectedValueOnce(new Error('unknown')); const first=make(); await fillReclass(first); await first.get('[data-test=reclass-submit]').trigger('click'); await settle(); const key=localStorage.key(0)!; const raw=localStorage.getItem(key)!; first.unmount(); localStorage.removeItem(key); sessionStorage.setItem('funding-admin-operation:42',raw)
    const second=make(); await settle(); expect(localStorage.getItem(key)).toBe(raw); expect(sessionStorage.getItem('funding-admin-operation:42')).toBeNull(); expect(second.find('[data-test=reclass-retry]').exists()).toBe(true); second.unmount()
  })
  it('fails closed when cross-tab coordination is unavailable', async () => {
    Object.defineProperty(navigator,'locks',{configurable:true,value:undefined}); const w=make(); await fillReclass(w); await w.get('[data-test=reclass-submit]').trigger('click'); await settle(); expect(mocks.post).not.toHaveBeenCalled(); expect(pendingCount()).toBe(0); expect(w.text()).toContain('adminFundingTools.storageBlocked'); w.unmount()
  })
  it('does not dispatch a queued retry after another tab definitively rejects the operation', async () => {
    mocks.post.mockRejectedValueOnce(new Error('unknown')); const initial=make(); await fillReclass(initial); await initial.get('[data-test=reclass-submit]').trigger('click'); await settle(); const original=mocks.post.mock.calls[0]; initial.unmount()
    let reject!: (value: unknown) => void; mocks.post.mockImplementationOnce(() => new Promise((_resolve, fail) => { reject=fail }))
    const first=make(),second=make(); await fillReclass(first); await fillReclass(second); await first.get('[data-test=reclass-retry]').trigger('click'); await settle(); await second.get('[data-test=reclass-retry]').trigger('click'); await settle(); expect(mocks.post).toHaveBeenCalledTimes(2)
    reject({status:400,reason:'FUNDING_OPERATION_REJECTED'}); await settle(); expect(mocks.post).toHaveBeenCalledTimes(2); expect(pendingCount()).toBe(0); expect(second.text()).toContain('adminFundingTools.resolvedElsewhere')
    await second.get('[data-test=reclass-order]').setValue('21'); mocks.post.mockResolvedValueOnce({}); await second.get('[data-test=reclass-submit]').trigger('click'); await settle(); expect(mocks.post).toHaveBeenCalledTimes(3); expect(mocks.post.mock.calls[2][2]).not.toBe(original[2]); first.unmount(); second.unmount()
  })
  it('does not apply a late response after switching users', async () => {
    let finish!: (value: unknown) => void; mocks.post.mockImplementationOnce(() => new Promise(resolve => { finish = resolve }))
    const w = make(); await fillReclass(w); await w.get('[data-test=reclass-submit]').trigger('click'); await settle(); const oldKey = mocks.post.mock.calls[0][2]
    mocks.auth.user = { id: 84 }; await settle(); finish({ user_id: 10, order_id: 20, amount: '1.25' }); await settle(); expect(w.text()).not.toContain('1.25'); expect(JSON.stringify(localStorage)).toContain(oldKey); expect(mocks.auth.user.id).toBe(84); w.unmount()
  })
})
