import { mount, flushPromises } from '@vue/test-utils'
import { describe, it, expect, vi, beforeEach } from 'vitest'
import AccountShareWizard from '../AccountShareWizard.vue'
const api = vi.hoisted(() => ({ createAccount: vi.fn(), createAuthorization: vi.fn(), completeAuthorization: vi.fn(), getAccount: vi.fn(), updateAccount: vi.fn(), getAuthorizationStatus: vi.fn(), getSupplierOperation: vi.fn() }))
vi.mock('@/api/supplier', () => api)
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
const routeGuard = vi.hoisted(() => ({ leave: null as null | (() => boolean), update: null as null | (() => boolean) }))
vi.mock('vue-router', () => ({ onBeforeRouteLeave: (fn: () => boolean) => { routeGuard.leave = fn }, onBeforeRouteUpdate: (fn: () => boolean) => { routeGuard.update = fn } }))
vi.mock('@/stores/workMode', () => ({ useWorkModeStore: () => ({ dirty: false, confirmLeave: async () => true }) }))
function setup(type: 'api_key' | 'oauth' = 'api_key', agreement = true) {
  return mount(AccountShareWizard, { props: { options: { providers: [{ platform: 'openai', label: 'OpenAI', account_types: [type], models: ['model'], prices: [{ model: 'model', public_model: 'model', group_name: 'pool', price_version: 'v1', rate_multiplier: '1', tiers: [{ from_tokens: 0, input_per_million: '1.00000000', output_per_million: '2.00000000', cache_read_per_million: '0.1', cache_write_per_million: '0.2' }] }] }], application_fields: [], agreement_text: agreement ? 'Terms' : '', max_supplier_concurrency: 4, agreement_version: 1, fee_bps: 1000, hold_hours: 24 } }, global: { stubs: { BaseDialog: { template: '<div><slot/></div>' }, RouterLink: { template: '<a><slot/></a>' } } } })
}
async function configure(wrapper: ReturnType<typeof setup>, type: 'api_key' | 'oauth' = 'api_key') {
  if (type === 'api_key') await wrapper.get('[data-test="api-key"]').setValue('secret')
  await wrapper.get('[data-test="next"]').trigger('click')
  await wrapper.get('[data-test="name"]').setValue('My account')
  await wrapper.get('[type=checkbox]').setValue(true)
  await wrapper.get('[data-test="next"]').trigger('click')
  await wrapper.get('[type=checkbox]').setValue(true)
}
beforeEach(() => { vi.resetAllMocks(); localStorage.clear() })
describe('account share wizard', () => {
  it('retains non-sensitive fields when authorization fails and clears the key', async () => {
    api.createAccount.mockRejectedValue({ status: 400 })
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
    expect(wrapper.find('input[autocomplete=off]').exists()).toBe(false)
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
    api.getAccount.mockResolvedValueOnce({ id: 42, name: 'Old', concurrency: 1, offer: { version: 1, allowed_models: ['model'] } }).mockResolvedValue({ id: 42, name: 'Other', concurrency: 1, offer: { version: 2, allowed_models: ['model'] } })
    api.updateAccount.mockRejectedValueOnce({ status: 409 }).mockResolvedValueOnce({})
    const wrapper = setup('oauth'); await configure(wrapper, 'oauth'); await wrapper.get('form').trigger('submit'); await flushPromises()
    expect(wrapper.get('[data-test="authorization-link"]').attributes('href')).toBe('https://example.test/auth')
    await wrapper.get('input[autocomplete=off]').setValue('https://example.test/callback?state=good&code=callback-code')
    await wrapper.get('form').trigger('submit'); await flushPromises()
    expect(wrapper.text()).toContain('marketplace.versionConflict')
    expect(wrapper.text()).not.toContain('must-not-render')
    await wrapper.get('[data-test="acknowledge-conflict"]').trigger('click')
    await wrapper.get('form').trigger('submit'); await flushPromises()
    expect(api.completeAuthorization).toHaveBeenCalledTimes(1)
    expect(wrapper.emitted('created')?.[0]).toEqual([42])
  })

  it('clears consumed authorization sessions after definitive state rejection', async () => {
    api.createAuthorization.mockResolvedValue({ auth_url: 'https://example.test/auth', session_id: 'old' }); api.completeAuthorization.mockRejectedValue({ status: 400 })
    const wrapper = setup('oauth'); await configure(wrapper, 'oauth'); await wrapper.get('form').trigger('submit'); await flushPromises()
    await wrapper.get('input[autocomplete=off]').setValue('https://example.test/?state=bad&code=bad'); await wrapper.get('form').trigger('submit'); await flushPromises()
    expect(wrapper.find('[data-test="authorization-link"]').exists()).toBe(false)
    expect(wrapper.get('[data-test="create"]').text()).toContain('marketplace.startAuthorization')
  })
  it('checks durable status instead of repeating an uncertain authorization completion', async () => {
    api.createAuthorization.mockResolvedValue({ auth_url: 'https://example.test/auth', session_id: 's1' }); api.completeAuthorization.mockRejectedValue({ code: 'ETIMEDOUT' }); api.getAuthorizationStatus.mockResolvedValue({ status: 'pending' })
    const wrapper = setup('oauth'); await configure(wrapper, 'oauth'); await wrapper.get('form').trigger('submit'); await flushPromises()
    await wrapper.get('input[autocomplete=off]').setValue('https://example.test/?state=x&code=y'); await wrapper.get('form').trigger('submit'); await flushPromises()
    expect(wrapper.text()).toContain('marketplace.authorizationUnknown'); expect(api.getAuthorizationStatus).toHaveBeenCalledWith('s1')
    await wrapper.get('[data-test="check-authorization"]').trigger('click'); await flushPromises()
    expect(api.completeAuthorization).toHaveBeenCalledTimes(1); expect(api.getAuthorizationStatus).toHaveBeenCalledWith('s1')
  })
  it('disables submission when any selected model has no published price', async () => {
    const wrapper = setup(); await configure(wrapper)
    await wrapper.setProps({ options: { ...wrapper.props('options'), providers: [{ ...wrapper.props('options').providers[0], prices: [] }] } })
    expect(wrapper.text()).toContain('marketplace.priceUnavailable')
    expect(wrapper.get('[data-test="create"]').attributes('disabled')).toBeDefined()
  })
  it('blocks modal closing and route departure while a creation is in flight', async () => {
    let resolve!: (value: unknown) => void; api.createAccount.mockReturnValue(new Promise(r => { resolve = r }))
    const wrapper = setup(); await configure(wrapper); await wrapper.get('form').trigger('submit')
    expect(routeGuard.leave?.()).toBe(false); expect(routeGuard.update?.()).toBe(false)
    expect(wrapper.get('[data-test="cancel"]').attributes('disabled')).toBeDefined()
    await wrapper.get('[data-test="cancel"]').trigger('click'); expect(wrapper.emitted('close')).toBeUndefined()
    resolve({}); await flushPromises()
  })

  it('keeps an uncertain creation locked and checks its idempotent operation', async () => {
    api.createAccount.mockRejectedValue({ code: 'ETIMEDOUT' }); api.getSupplierOperation.mockResolvedValue({ status: 'completed', account_id: 9 })
    const wrapper = setup(); await configure(wrapper); await wrapper.get('form').trigger('submit'); await flushPromises()
    expect(wrapper.text()).toContain('marketplace.operationUnknown')
    expect(routeGuard.leave?.()).toBe(false)
    await wrapper.get('[data-test="check-operation"]').trigger('click'); await flushPromises()
    expect(api.createAccount).toHaveBeenCalledTimes(1); expect(wrapper.emitted('created')?.[0]).toEqual([9])
  })

  it('shows exact published pricing before accepting the policy', async () => {
    const wrapper = setup(); await configure(wrapper)
    expect(wrapper.text()).toContain('1.00000000'); expect(wrapper.text()).toContain('2.00000000'); expect(wrapper.text()).toContain('pool'); expect(wrapper.text()).toContain('v1')
  })
  it('unlocks only when the server confirms the uncertain OAuth session failed', async () => {
    api.createAuthorization.mockResolvedValue({ auth_url: 'https://example.test/auth', session_id: 's1' }); api.completeAuthorization.mockRejectedValue({ code: 'ETIMEDOUT' }); api.getAuthorizationStatus.mockResolvedValueOnce({ status: 'pending' }).mockResolvedValue({ status: 'failed' })
    const wrapper = setup('oauth'); await configure(wrapper, 'oauth'); await wrapper.get('form').trigger('submit'); await flushPromises()
    await wrapper.get('input[autocomplete=off]').setValue('https://example.test/?state=x&code=y'); await wrapper.get('form').trigger('submit'); await flushPromises()
    await wrapper.get('[data-test="check-authorization"]').trigger('click'); await flushPromises()
    expect(wrapper.text()).not.toContain('marketplace.authorizationUnknown'); expect(wrapper.get('[data-test="create"]').text()).toContain('marketplace.startAuthorization')
  })

  it('releases the navigation guard once OAuth creation is confirmed', async () => {
    api.createAuthorization.mockResolvedValue({ auth_url: 'https://example.test/auth', session_id: 'session' }); api.completeAuthorization.mockResolvedValue({ account_id: 42 }); api.getAccount.mockResolvedValue({ id: 42, offer: { version: 1 } }); api.updateAccount.mockResolvedValue({})
    const wrapper = setup('oauth'); let allowed: boolean | undefined; await wrapper.setProps({ onCreated: () => { allowed = routeGuard.leave?.() } }); await configure(wrapper, 'oauth'); await wrapper.get('form').trigger('submit'); await flushPromises()
    await wrapper.get('input[autocomplete=off]').setValue('https://example.test/?state=x&code=y'); await wrapper.get('form').trigger('submit'); await flushPromises()
    expect(wrapper.emitted('created')?.[0]).toEqual([42]); expect(allowed).toBe(true)
  })

  it('retries the original authorized-account CAS after an unchanged reread', async () => {
    api.createAuthorization.mockResolvedValue({ auth_url: 'https://example.test/auth', session_id: 'session' }); api.completeAuthorization.mockResolvedValue({ account_id: 42 }); api.getAccount.mockResolvedValue({ id: 42, name: 'Old', concurrency: 1, offer: { version: 1, allowed_models: ['model'] } }); api.updateAccount.mockRejectedValueOnce({ code: 'ETIMEDOUT' }).mockResolvedValueOnce({})
    const wrapper = setup('oauth'); await configure(wrapper, 'oauth'); await wrapper.get('form').trigger('submit'); await flushPromises(); await wrapper.get('input[autocomplete=off]').setValue('https://example.test/?state=x&code=y'); await wrapper.get('form').trigger('submit'); await flushPromises()
    await wrapper.get('[data-test="check-operation"]').trigger('click'); await flushPromises(); expect(wrapper.text()).toContain('marketplace.operationUnknown')
    await wrapper.get('[data-test="retry-patch"]').trigger('click'); await flushPromises()
    expect(api.updateAccount).toHaveBeenCalledTimes(2); expect(api.updateAccount.mock.calls[1]).toEqual(api.updateAccount.mock.calls[0]); expect(api.completeAuthorization).toHaveBeenCalledTimes(1); expect(wrapper.emitted('created')?.[0]).toEqual([42])
  })
  it('unlocks an authorized-account CAS conflict without losing its configuration', async () => {
    api.createAuthorization.mockResolvedValue({ auth_url: 'https://example.test/auth', session_id: 'session' }); api.completeAuthorization.mockResolvedValue({ account_id: 42 }); api.getAccount.mockResolvedValueOnce({ id: 42, name: 'Old', concurrency: 1, offer: { version: 1, allowed_models: ['model'] } }).mockResolvedValueOnce({ id: 42, name: 'Other', concurrency: 1, offer: { version: 2, allowed_models: ['model'] } }); api.updateAccount.mockRejectedValue({ code: 'ETIMEDOUT' })
    const wrapper = setup('oauth'); await configure(wrapper, 'oauth'); await wrapper.get('form').trigger('submit'); await flushPromises(); await wrapper.get('input[autocomplete=off]').setValue('https://example.test/?state=x&code=y'); await wrapper.get('form').trigger('submit'); await flushPromises()
    await wrapper.get('[data-test="check-operation"]').trigger('click'); await flushPromises()
    expect(wrapper.text()).toContain('marketplace.versionConflict'); expect(wrapper.text()).not.toContain('marketplace.operationUnknown'); expect(wrapper.text()).toContain('Other'); expect(wrapper.get('[data-test="create"]').attributes('disabled')).toBeDefined()
  })

})
