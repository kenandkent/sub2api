import { describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import ReviewList from '../ReviewList.vue'

const api = vi.hoisted(() => ({ list: vi.fn(), post: vi.fn() }))
const stepRun = vi.hoisted(() => vi.fn((action: () => Promise<unknown>) => action()))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => ({ user: { id: 42 } }) }))
vi.mock('@/composables/useStepUp', () => ({ useStepUp: () => ({ run: stepRun }), isStepUpCancelled: () => false }))
vi.mock('vue-i18n', async importOriginal => ({ ...(await importOriginal<typeof import('vue-i18n')>()), useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@/api/admin/marketplace', () => ({ ...api, items: (value: any) => value.items }))
const stubs = {
  AppLayout: { template: '<div><slot /></div>' },
  TablePageLayout: { template: '<div><slot name="filters" /><slot name="table" /><slot name="pagination" /></div>' },
  DataTable: { props: ['data', 'columns', 'loading'], template: '<div><div v-for="row in data" :key="row.id"><slot name="cell-actions" :row="row" /></div></div>' },
  TotpStepUpDialog: true,
  BaseDialog: { props: ['show'], template: '<div v-if="show"><slot /><slot name="footer" /></div>' }
}
describe('offer review', () => {
  it('submits the offer ID and expected version rather than the account ID', async () => {
    api.list.mockResolvedValue({ items: [{ id: 17, account_id: 99, owner_user_id: 2, version: 4, review_status: 'pending' }], total: 1 })
    api.post.mockResolvedValue({})
    const wrapper = mount(ReviewList, { props: { kind: 'offers' }, global: { stubs } }); await flushPromises()
    await wrapper.findAll('button').find(b => b.text() === 'adminMarketplace.review')!.trigger('click')
    await wrapper.find('textarea').setValue('Approved for sharing')
    expect(wrapper.findAll('button').find(b => b.text() === 'adminMarketplace.confirmReview')!.attributes('disabled')).toBeDefined()
    await wrapper.get('[data-test=identity-reviewed]').setValue(true)
    await wrapper.findAll('button').find(b => b.text() === 'adminMarketplace.confirmReview')!.trigger('click'); await flushPromises()
    expect(api.post).toHaveBeenCalledWith('offers/17/review', { status: 'approved', reason: 'Approved for sharing', expected_version: 4, identity_reviewed: true })
  })
})

it('sends the reviewed supplier version with its decision', async () => {
  api.list.mockResolvedValue({ items: [{ user_id: 8, version: 4, status: 'pending', payout_frozen: false, application_fields: {} }], total: 1 }); api.post.mockResolvedValue({})
  const wrapper = mount(ReviewList, { props: { kind: 'suppliers' }, global: { stubs } }); await flushPromises()
  await wrapper.findAll('button').find(b => b.text() === 'adminMarketplace.review')!.trigger('click'); await wrapper.find('textarea').setValue('Approved')
  await wrapper.findAll('button').find(b => b.text() === 'adminMarketplace.confirmReview')!.trigger('click'); await flushPromises()
  expect(api.post).toHaveBeenCalledWith('suppliers/8/review', { status: 'active', reason: 'Approved', expected_version: 4 })
})

describe('supplier payout freeze', () => {
  it('retries an uncertain freeze using the same key and payload', async () => {
    api.list.mockResolvedValue({ items: [{ user_id: 8, version: 4, payout_frozen: false, application_fields: {} }], total: 1 })
    api.post.mockRejectedValueOnce({ status: 0 }).mockResolvedValueOnce({})
    const w = mount(ReviewList, { props: { kind: 'suppliers' }, global: { stubs } }); await flushPromises()
    await w.get('[data-test=payout-freeze]').trigger('click')
    await w.get('[data-test=freeze-reason]').setValue('Review risk')
    await w.get('[data-test=submit-freeze]').trigger('click'); await flushPromises()
    expect(api.post).toHaveBeenCalledWith('suppliers/8/payout-freeze', { expected_version: 4, frozen: true, reason: 'Review risk' }, expect.any(String))
    expect(w.get('[data-test=freeze-reason]').attributes('disabled')).toBeDefined()
    await w.get('[data-test=submit-freeze]').trigger('click'); await flushPromises()
    expect(api.post.mock.calls.at(-1)).toEqual(api.post.mock.calls.at(-2))
    w.unmount()
  })
  it('offers an unfreeze attempt and surfaces the server debt conflict', async () => {
    api.list.mockResolvedValue({ items: [{ user_id: 9, version: 5, payout_frozen: true, application_fields: {} }], total: 1 })
    api.post.mockRejectedValueOnce({ status: 409, message: 'Outstanding debt' })
    const w = mount(ReviewList, { props: { kind: 'suppliers' }, global: { stubs } }); await flushPromises()
    await w.get('[data-test=payout-freeze]').trigger('click'); await w.get('[data-test=freeze-reason]').setValue('Debt reviewed')
    await w.get('[data-test=submit-freeze]').trigger('click'); await flushPromises()
    expect(api.post).toHaveBeenCalledWith('suppliers/9/payout-freeze', { expected_version: 5, frozen: false, reason: 'Debt reviewed' }, expect.any(String))
    expect(w.text()).toContain('Outstanding debt')
    w.unmount()
  })
  it('retains an uncertain freeze across reopening the page', async () => {
    sessionStorage.clear()
    api.list.mockResolvedValue({ items: [{ user_id: 11, version: 2, payout_frozen: false, application_fields: {} }], total: 1 })
    api.post.mockRejectedValueOnce({ status: 0 }).mockResolvedValueOnce({})
    const first = mount(ReviewList, { props: { kind: 'suppliers' }, global: { stubs } }); await flushPromises()
    await first.get('[data-test=payout-freeze]').trigger('click'); await first.get('[data-test=freeze-reason]').setValue('Review')
    await first.get('[data-test=submit-freeze]').trigger('click'); await flushPromises(); const original = api.post.mock.calls.at(-1); first.unmount()
    const second = mount(ReviewList, { props: { kind: 'suppliers' }, global: { stubs } }); await flushPromises()
    await second.get('[data-test=payout-freeze]').trigger('click')
    expect(second.get('[data-test=freeze-reason]').attributes('disabled')).toBeDefined()
    await second.get('[data-test=submit-freeze]').trigger('click'); await flushPromises()
    expect(api.post.mock.calls.at(-1)).toEqual(original)
    second.unmount(); sessionStorage.clear()
  })

})
