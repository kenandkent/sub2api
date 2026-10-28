import { mount, flushPromises } from '@vue/test-utils'
import { describe, it, expect, vi, beforeEach } from 'vitest'
import AccountShareWizard from '../AccountShareWizard.vue'
const api = vi.hoisted(() => ({ createAccount: vi.fn(), createAuthorization: vi.fn(), completeAuthorization: vi.fn(), getAccount: vi.fn(), updateAccount: vi.fn() }))
vi.mock('@/api/supplier', () => api)
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@/stores/workMode', () => ({ useWorkModeStore: () => ({ dirty: false, confirmLeave: async () => true }) }))
function setup(type: 'api_key' | 'oauth' = 'api_key', agreement = true) {
  return mount(AccountShareWizard, { props: { options: { providers: [{ platform: 'openai', label: 'OpenAI', account_types: [type], models: ['model'] }], application_fields: [], agreement_text: agreement ? 'Terms' : '', max_supplier_concurrency: 4, agreement_version: 1, fee_bps: 1000, hold_hours: 24 } }, global: { stubs: { BaseDialog: { template: '<div><slot/></div>' }, RouterLink: { template: '<a><slot/></a>' } } } })
}
async function configure(wrapper: ReturnType<typeof setup>, type: 'api_key' | 'oauth' = 'api_key') {
  if (type === 'api_key') await wrapper.get('[data-test="api-key"]').setValue('secret')
  await wrapper.get('[data-test="next"]').trigger('click')
  await wrapper.get('[data-test="name"]').setValue('My account')
  await wrapper.get('[type=checkbox]').setValue(true)
  await wrapper.get('[data-test="next"]').trigger('click')
  await wrapper.get('[type=checkbox]').setValue(true)
}
beforeEach(() => { vi.clearAllMocks(); localStorage.clear() })
describe('account share wizard', () => {
  it('retains non-sensitive fields when authorization fails and clears the key', async () => {
    api.createAccount.mockRejectedValue(new Error('failure'))
    const wrapper = setup(); await configure(wrapper); await wrapper.get('form').trigger('submit'); await flushPromises()
    expect(wrapper.text()).toContain('marketplace.authorizationFailed')
    await wrapper.get('[data-test="back"]').trigger('click')
    expect((wrapper.get('[data-test="name"]').element as HTMLInputElement).value).toBe('My account')
    await wrapper.get('[data-test="back"]').trigger('click')
    expect((wrapper.get('[data-test="api-key"]').element as HTMLInputElement).value).toBe('')
  })
  it('reports a rejected OAuth state and never displays callback credentials', async () => {
    api.createAuthorization.mockResolvedValue({ auth_url: 'https://example.test/auth', session_id: 'session' }); api.completeAuthorization.mockRejectedValue({ status: 400 }); vi.spyOn(window, 'open').mockImplementation(() => null)
    const wrapper = setup('oauth'); await configure(wrapper, 'oauth'); await wrapper.get('form').trigger('submit'); await flushPromises()
    await wrapper.get('input[autocomplete=off]').setValue('https://example.test/callback?state=bad&code=secretcode')
    await wrapper.get('form').trigger('submit'); await flushPromises()
    expect(wrapper.text()).toContain('marketplace.authorizationFailed')
    expect((wrapper.get('input[autocomplete=off]').element as HTMLInputElement).value).toBe('')
    expect(wrapper.text()).not.toContain('secretcode')
  })
  it('disables submission when the supplier agreement is unavailable', async () => {
    const wrapper = setup('api_key', false); await configure(wrapper)
    expect(wrapper.text()).toContain('marketplace.agreementUnavailable')
    expect(wrapper.get('[data-test="create"]').attributes('disabled')).toBeDefined()
  })
  it('preserves the name and model selection when navigating back between steps', async () => {
    const wrapper = setup(); await wrapper.get('[data-test="api-key"]').setValue('secret'); await wrapper.get('[data-test="next"]').trigger('click')
    await wrapper.get('[data-test="name"]').setValue('My account'); await wrapper.get('[type=checkbox]').setValue(true)
    await wrapper.get('[data-test="back"]').trigger('click'); await wrapper.get('[data-test="next"]').trigger('click')
    expect((wrapper.get('[data-test="name"]').element as HTMLInputElement).value).toBe('My account')
    expect((wrapper.get('[type=checkbox]').element as HTMLInputElement).checked).toBe(true)
    expect(JSON.stringify(localStorage)).not.toContain('secret')
  })
  it('offers the server authorization link and retries a conflicting configuration without creating another account', async () => {
    api.createAuthorization.mockResolvedValue({ auth_url: 'https://example.test/auth', session_id: 'session' })
    api.completeAuthorization.mockResolvedValue({ account_id: 42, access_token: 'must-not-render' })
    api.getAccount.mockResolvedValue({ id: 42, offer: { version: 2 } })
    api.updateAccount.mockRejectedValueOnce({ status: 409 }).mockResolvedValueOnce({})
    const wrapper = setup('oauth'); await configure(wrapper, 'oauth'); await wrapper.get('form').trigger('submit'); await flushPromises()
    expect(wrapper.get('[data-test="authorization-link"]').attributes('href')).toBe('https://example.test/auth')
    await wrapper.get('input[autocomplete=off]').setValue('https://example.test/callback?state=good&code=callback-code')
    await wrapper.get('form').trigger('submit'); await flushPromises()
    expect(wrapper.text()).toContain('marketplace.versionConflict')
    expect(wrapper.text()).not.toContain('must-not-render')
    await wrapper.get('form').trigger('submit'); await flushPromises()
    expect(api.completeAuthorization).toHaveBeenCalledTimes(1)
    expect(wrapper.emitted('created')?.[0]).toEqual([42])
  })

})
