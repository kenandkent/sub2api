import { mount, flushPromises } from '@vue/test-utils'
import { describe, it, expect, vi } from 'vitest'
import AccountShareWizard from '../AccountShareWizard.vue'
const api = vi.hoisted(() => ({ createAccount: vi.fn(), createAuthorization: vi.fn(), completeAuthorization: vi.fn(), getAccount: vi.fn() }))
vi.mock('@/api/supplier', () => api)
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@/stores/workMode', () => ({ useWorkModeStore: () => ({ dirty: false }) }))
describe('account share wizard', () => {
  it('retains non-sensitive fields when authorization fails', async () => {
    api.createAccount.mockRejectedValue(new Error('failure'))
    const wrapper = mount(AccountShareWizard, { props: { options: { providers: [{ platform: 'openai', label: 'OpenAI', account_types: ['api_key'], models: ['model'] }], application_fields: [], agreement_text: 'Terms', max_supplier_concurrency: 4, agreement_version: 1, fee_bps: 1000, hold_hours: 24 } }, global: { stubs: { BaseDialog: { template: '<div><slot/><slot name="footer"/></div>' }, RouterLink: { template: '<a><slot/></a>' } } } })
    await wrapper.get('[data-test="name"]').setValue('My account')
    await wrapper.get('[type=checkbox]').setValue(true)
    await wrapper.get('[data-test="api-key"]').setValue('secret')
    await wrapper.findAll('[type=checkbox]')[1].setValue(true)
    await wrapper.get('form').trigger('submit'); await flushPromises()
    expect((wrapper.get('[data-test="name"]').element as HTMLInputElement).value).toBe('My account')
    expect(wrapper.text()).toContain('marketplace.authorizationFailed')
  })
  it('reports a rejected OAuth state and never displays callback credentials', async () => {
    api.createAuthorization.mockResolvedValue({ auth_url: 'https://example.test/auth', session_id: 'session' })
    api.completeAuthorization.mockRejectedValue({ status: 400 })
    vi.spyOn(window, 'open').mockImplementation(() => null)
    const wrapper = mount(AccountShareWizard, { props: { options: { providers: [{ platform: 'openai', label: 'OpenAI', account_types: ['oauth'], models: ['model'] }], application_fields: [], agreement_text: 'Terms', max_supplier_concurrency: 4, agreement_version: 1, fee_bps: 1000, hold_hours: 24 } }, global: { stubs: { BaseDialog: { template: '<div><slot/><slot name="footer"/></div>' }, RouterLink: { template: '<a><slot/></a>' } } } })
    await wrapper.get('[type=checkbox]').setValue(true)
    await wrapper.findAll('button').find(b => b.text() === 'marketplace.startAuthorization')!.trigger('click'); await flushPromises()
    await wrapper.get('input[autocomplete=off]').setValue('https://example.test/callback?state=bad&code=secretcode')
    await wrapper.findAll('button').find(b => b.text() === 'marketplace.completeAuthorization')!.trigger('click'); await flushPromises()
    expect(wrapper.text()).toContain('marketplace.authorizationFailed')
    expect((wrapper.get('input[autocomplete=off]').element as HTMLInputElement).value).toBe('')
    expect(wrapper.text()).not.toContain('secretcode')
  })

  it('disables submission when the supplier agreement is unavailable', async () => {
    const wrapper = mount(AccountShareWizard, { props: { options: { providers: [{ platform: 'openai', label: 'OpenAI', account_types: ['api_key'], models: ['model'] }], application_fields: [], max_supplier_concurrency: 4, agreement_version: 1, fee_bps: 1000, hold_hours: 24 } }, global: { stubs: { BaseDialog: { template: '<div><slot/><slot name="footer"/></div>' }, RouterLink: { template: '<a><slot/></a>' } } } })
    await wrapper.get('[data-test="name"]').setValue('Name')
    await wrapper.findAll('[type=checkbox]')[0].setValue(true)
    await wrapper.get('[data-test="api-key"]').setValue('secret')
    await wrapper.findAll('[type=checkbox]')[1].setValue(true)
    expect(wrapper.text()).toContain('marketplace.agreementUnavailable')
    expect(wrapper.get('[data-test="create"]').attributes('disabled')).toBeDefined()
  })

})
