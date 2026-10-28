import { flushPromises, mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import StatementsView from '../StatementsView.vue'
const api = vi.hoisted(() => ({ listStatements: vi.fn(), getStatement: vi.fn(), exportStatement: vi.fn(), listStatementEntries: vi.fn() }))
vi.mock('@/api/supplier', () => api)
vi.mock('vue-i18n', async () => ({ ...(await vi.importActual<typeof import('vue-i18n')>('vue-i18n')), useI18n: () => ({ t: (key: string) => key }) }))
describe('statements', () => {
  it('loads a selected statement and shows exact closing and refund amounts', async () => {
    const row = { id: 1, month: '2026-01', version: 1, closing_available: '123456789012345.12345678', refund_net: '0.0123' }
    api.listStatementEntries.mockResolvedValue({ items: [{ operation_id: 'refund-1', kind: 'refund', model: 'model', account_name: 'Account', consumer_charge: '0.0123', supplier_net: '0.01', platform_fee: '0.0023' }], page: 1, pages: 1, total: 1 }); api.listStatements.mockResolvedValue({ items: [row], page: 1, pages: 1 }); api.getStatement.mockResolvedValue(row)
    const w = mount(StatementsView, { global: { stubs: { BaseDialog: { props: ['show'], template: '<div v-if="show"><slot /></div>' }, AppLayout: { template: '<div><slot /></div>' }, TablePageLayout: { template: '<div><slot name="filters"/><slot name="table"/></div>' } } } }); await flushPromises()
    await w.get('[data-test=statement-detail]').trigger('click'); await flushPromises()
    expect(w.text()).toContain('123456789012345.12345678'); expect(w.text()).toContain('0.0123'); expect(w.text()).toContain('refund-1')
  })
  it('does not mix a previous statement’s delayed entries with a newly selected statement', async () => {
    let resolveFirst!: (value: unknown) => void
    const a = { id: 1, month: '2026-01', version: 1 }, b = { id: 2, month: '2026-02', version: 1 }
    api.listStatements.mockResolvedValue({ items: [a, b], page: 1, pages: 1 }); api.getStatement.mockImplementation(async (id: number) => id === 1 ? a : b)
    api.listStatementEntries.mockImplementation((id: number) => id === 1 ? new Promise(r => { resolveFirst = r }) : Promise.resolve({ items: [{ operation_id: 'B-entry', kind: 'capture' }], page: 1, pages: 1 }))
    const w = mount(StatementsView, { global: { stubs: { BaseDialog: { props: ['show'], template: '<div v-if="show"><slot /></div>' }, AppLayout: { template: '<div><slot /></div>' }, TablePageLayout: { template: '<div><slot name="filters"/><slot name="table"/></div>' } } } }); await flushPromises()
    await w.findAll('[data-test=statement-detail]')[0].trigger('click'); await flushPromises()
    await w.findAll('[data-test=statement-detail]')[1].trigger('click'); await flushPromises()
    resolveFirst({ items: [{ operation_id: 'A-entry', kind: 'capture' }], page: 1, pages: 1 }); await flushPromises()
    expect(w.text()).toContain('2026-02'); expect(w.text()).toContain('B-entry'); expect(w.text()).not.toContain('A-entry')
  })
})
