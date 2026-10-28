import { beforeEach, describe, expect, it, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import ExecutionIncidentDialog from '../ExecutionIncidentDialog.vue'
const state = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn(), run: vi.fn() }))
vi.mock('@/api/admin/marketplace', () => ({ get: state.get, post: state.post }))
vi.mock('vue-i18n', async original => ({ ...await original<typeof import('vue-i18n')>(), useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@/composables/useStepUp', () => ({ useStepUp: () => ({ run: state.run }), isStepUpCancelled: () => false }))
const receipt = '12345678-1234-4234-9234-123456789abc'
function setup() { return mount(ExecutionIncidentDialog, { props: { show: true }, global: { stubs: { BaseDialog: { props: ['show'], template: '<div v-if="show"><slot /><slot name="footer" /></div>' }, TotpStepUpDialog: true } } }) }
async function fill(wrapper: ReturnType<typeof setup>) {
  await wrapper.find('[data-test="incident-receipt"]').setValue(receipt)
  await wrapper.find('[data-test="incident-attempt"]').setValue('12')
  await wrapper.find('[data-test="incident-release"]').setValue('release-1')
  await wrapper.find('textarea').setValue('Verified provider case reference')
}
describe('manual execution incident evidence', () => {
  beforeEach(() => { state.get.mockReset().mockResolvedValue({ items: [] }); state.post.mockReset().mockResolvedValue({ status: 'recorded_manual_review' }); state.run.mockReset().mockImplementation((fn: () => Promise<unknown>) => fn()) })
  it('requires receipt, attempt, release audit and evidence; has no amount or earnings control', async () => {
    const wrapper = setup()
    expect(wrapper.find('[data-test="incident-submit"]').attributes('disabled')).toBeDefined()
    await fill(wrapper)
    expect(wrapper.find('[data-test="incident-submit"]').attributes('disabled')).toBeUndefined()
    expect(wrapper.find('input[inputmode="decimal"]').exists()).toBe(false)
    expect(wrapper.text()).toContain('adminMarketplace.incidentRecordOnly')
    await wrapper.find('textarea').setValue('界'.repeat(1334))
    expect(wrapper.find('[data-test="incident-submit"]').attributes('disabled')).toBeDefined()
    wrapper.unmount()
  })
  it('retains the same evidence and operation key after a network-unknown result', async () => {
    state.post.mockRejectedValueOnce(new Error('network')).mockResolvedValueOnce({ status: 'recorded_manual_review' })
    const wrapper = setup(); await fill(wrapper)
    await wrapper.find('[data-test="incident-submit"]').trigger('click'); await flushPromises()
    expect(wrapper.find('[data-test="incident-receipt"]').attributes('disabled')).toBeDefined()
    await wrapper.find('[data-test="incident-submit"]').trigger('click'); await flushPromises()
    expect(state.run).toHaveBeenCalledTimes(2)
    expect(state.post.mock.calls[0]).toEqual(state.post.mock.calls[1])
    expect(state.post.mock.calls[0][0]).toBe(`receipts/${receipt}/execution-incidents`)
    expect(state.post.mock.calls[0][1]).toEqual({ attempt_id: 12, release_operation_id: 'release-1', evidence: 'Verified provider case reference' })
    expect(wrapper.text()).toContain('adminMarketplace.incidentRecorded')
    wrapper.unmount()
  })
  it('reconciles an unknown response from the saved operation without sending another POST', async () => {
    state.post.mockRejectedValueOnce(new Error('network'))
    const wrapper = setup(); await fill(wrapper)
    await wrapper.find('[data-test="incident-submit"]').trigger('click'); await flushPromises()
    const key = state.post.mock.calls[0][2]
    state.get.mockResolvedValue({ items: [{ operation_id: key, evidence: 'Verified provider case reference', attempt_id: 12, release_operation_id: 'release-1', status: 'recorded_manual_review' }] })
    await wrapper.find('[data-test="incident-check"]').trigger('click'); await flushPromises()
    expect(state.get).toHaveBeenCalledWith(`receipts/${receipt}/execution-incidents`)
    expect(state.post).toHaveBeenCalledOnce()
    expect(wrapper.text()).toContain('adminMarketplace.incidentRecorded')
    expect(wrapper.find('[data-test="incident-submit"]').attributes('disabled')).toBeDefined()
    wrapper.unmount()
  })
})
