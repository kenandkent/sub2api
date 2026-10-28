import { describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import ReviewList from '../ReviewList.vue'

const api = vi.hoisted(() => ({ list: vi.fn(), post: vi.fn() }))
vi.mock('vue-i18n', async importOriginal => ({ ...(await importOriginal<typeof import('vue-i18n')>()), useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@/api/admin/marketplace', () => ({ ...api, items: (value: any) => value.items }))
const stubs = {
  AppLayout: { template: '<div><slot /></div>' },
  TablePageLayout: { template: '<div><slot name="filters" /><slot name="table" /><slot name="pagination" /></div>' },
  DataTable: { props: ['data', 'columns', 'loading'], template: '<div><div v-for="row in data" :key="row.id"><slot name="cell-actions" :row="row" /></div></div>' },
  BaseDialog: { props: ['show'], template: '<div v-if="show"><slot /><slot name="footer" /></div>' }
}
describe('offer review', () => {
  it('submits the offer ID and expected version rather than the account ID', async () => {
    api.list.mockResolvedValue({ items: [{ id: 17, account_id: 99, owner_user_id: 2, version: 4, review_status: 'pending' }], total: 1 })
    api.post.mockResolvedValue({})
    const wrapper = mount(ReviewList, { props: { kind: 'offers' }, global: { stubs } }); await flushPromises()
    await wrapper.findAll('button').find(b => b.text() === 'adminMarketplace.review')!.trigger('click')
    await wrapper.find('textarea').setValue('Approved for sharing')
    await wrapper.findAll('button').find(b => b.text() === 'adminMarketplace.confirmReview')!.trigger('click'); await flushPromises()
    expect(api.post).toHaveBeenCalledWith('offers/17/review', { status: 'approved', reason: 'Approved for sharing', expected_version: 4 })
  })
})
