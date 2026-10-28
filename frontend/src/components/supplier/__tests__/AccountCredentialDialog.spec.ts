import { mount, flushPromises } from '@vue/test-utils'
import { it, expect, vi, beforeEach } from 'vitest'
import AccountCredentialDialog from '../AccountCredentialDialog.vue'
const api = vi.hoisted(() => ({ replaceCredentials: vi.fn(), getSupplierOperation: vi.fn(), createAuthorization: vi.fn(), completeAuthorization: vi.fn(), getAuthorizationStatus: vi.fn() }))
vi.mock('@/api/supplier', () => api)
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('vue-router', () => ({ onBeforeRouteLeave: vi.fn(), onBeforeRouteUpdate: vi.fn() }))
const account = { id: 1, name: 'name', platform: 'openai', type: 'api_key', concurrency: 1, status: 'active', credential_status: 'invalid', current_concurrency: 0, created_at: '', offer: { id: 3, review_status: 'approved', enabled: false, allowed_models: ['model'], version: 4 } }
const stubs = { BaseDialog: { template: '<div><slot /></div>' } }
beforeEach(() => vi.resetAllMocks())
it('uses the existing account and checks a timed-out API key replacement by operation ID', async () => {
  api.replaceCredentials.mockRejectedValue({ code: 'ETIMEDOUT' }); api.getSupplierOperation.mockResolvedValue({ status: 'completed', account_id: 1 })
  const wrapper = mount(AccountCredentialDialog, { props: { account }, global: { stubs } })
  await wrapper.get('[data-test="credential-key"]').setValue('secret'); await wrapper.get('form').trigger('submit'); await flushPromises()
  expect(api.replaceCredentials.mock.calls[0][0]).toBe(1); expect(api.replaceCredentials.mock.calls[0][1]).toEqual({ api_key: 'secret', expected_version: 4 })
  expect(wrapper.text()).toContain('marketplace.operationUnknown'); expect(wrapper.text()).not.toContain('secret')
  await wrapper.get('[data-test="check-credential"]').trigger('click'); await flushPromises()
  expect(api.replaceCredentials).toHaveBeenCalledTimes(1); expect(wrapper.emitted('saved')).toBeTruthy()
})
it('reauthorizes the same OAuth account and restarts a definitively rejected state', async () => {
  api.createAuthorization.mockResolvedValue({ auth_url: 'https://example.test/auth', session_id: 's1' }); api.completeAuthorization.mockRejectedValue({ status: 400 }); api.getAuthorizationStatus.mockResolvedValue({ status: 'failed' })
  const wrapper = mount(AccountCredentialDialog, { props: { account: { ...account, type: 'oauth' } }, global: { stubs } })
  await wrapper.get('form').trigger('submit'); await flushPromises()
  expect(api.createAuthorization).toHaveBeenCalledWith({ purpose: 'reauthorize', provider: 'openai', account_id: 1 })
  await wrapper.get('[data-test="credential-callback"]').setValue('https://example.test/?state=x&code=y'); await wrapper.get('form').trigger('submit'); await flushPromises()
  expect(wrapper.find('[data-test="credential-callback"]').exists()).toBe(false)
  expect(api.completeAuthorization).toHaveBeenCalledWith('s1', { state: 'x', code: 'y', purpose: 'reauthorize', account_id: 1 })
})

it('queries an uncertain OAuth completion instead of exchanging again', async () => {
  api.createAuthorization.mockResolvedValue({ auth_url: 'https://example.test/auth', session_id: 's1' }); api.completeAuthorization.mockRejectedValue({ code: 'ETIMEDOUT' }); api.getAuthorizationStatus.mockResolvedValueOnce({ status: 'pending' }).mockResolvedValue({ status: 'completed', account_id: 1 })
  const wrapper = mount(AccountCredentialDialog, { props: { account: { ...account, type: 'oauth' } }, global: { stubs } })
  await wrapper.get('form').trigger('submit'); await flushPromises(); await wrapper.get('[data-test="credential-callback"]').setValue('https://example.test/?state=x&code=y'); await wrapper.get('form').trigger('submit'); await flushPromises()
  expect(wrapper.text()).toContain('marketplace.authorizationUnknown'); expect(api.getAuthorizationStatus).toHaveBeenCalledWith('s1'); await wrapper.get('[data-test="check-credential"]').trigger('click'); await flushPromises()
  expect(api.completeAuthorization).toHaveBeenCalledTimes(1); expect(wrapper.emitted('saved')).toBeTruthy()
})

it('accepts a confirmed credential operation without an account reference', async () => {
  api.replaceCredentials.mockRejectedValue({ code: 'ETIMEDOUT' }); api.getSupplierOperation.mockResolvedValue({ status: 'completed' })
  const wrapper = mount(AccountCredentialDialog, { props: { account }, global: { stubs } }); await wrapper.get('[data-test="credential-key"]').setValue('secret'); await wrapper.get('form').trigger('submit'); await flushPromises(); await wrapper.get('[data-test="check-credential"]').trigger('click'); await flushPromises()
  expect(wrapper.emitted('saved')).toBeTruthy()
})

it('retains only the nonsecret operation reference and retries the same key after reopening', async () => {
  sessionStorage.clear()
  let resolve!: (value: unknown) => void; let retry: unknown
  api.replaceCredentials.mockImplementationOnce(() => new Promise(r => { resolve = r })).mockImplementationOnce(async (id, payload, op) => { retry = [id, { ...payload }, op]; return {} })
  const first = mount(AccountCredentialDialog, { props: { account: { ...account, type: 'apikey' }, ownerId: 42 }, global: { stubs } })
  await first.get('[data-test=credential-key]').setValue('key-value'); await first.get('form').trigger('submit'); await flushPromises()
  const op = api.replaceCredentials.mock.calls[0][2]
  expect(JSON.stringify(sessionStorage)).not.toContain('key-value')
  expect(JSON.stringify(sessionStorage)).toContain(op)
  first.unmount()
  const second = mount(AccountCredentialDialog, { props: { account: { ...account, type: 'apikey' }, ownerId: 42 }, global: { stubs } }); await flushPromises()
  await second.get('[data-test=credential-key]').setValue('key-value'); await second.get('[data-test=retry-credential]').trigger('click'); await flushPromises()
  expect(retry).toEqual([1, { api_key: 'key-value', expected_version: 4 }, op])
  resolve({}); second.unmount(); sessionStorage.clear()
})

it('restores OAuth completion and shows a manual review outcome without repeating the exchange', async () => {
  sessionStorage.clear(); api.createAuthorization.mockResolvedValue({ auth_url: 'https://example.test/auth', session_id: 'session-a' }); api.completeAuthorization.mockRejectedValue({ code: 'ETIMEDOUT' }); api.getAuthorizationStatus.mockResolvedValue({ status: 'needs_review' })
  const first = mount(AccountCredentialDialog, { props: { account: { ...account, type: 'oauth' }, ownerId: 42 }, global: { stubs } }); await first.get('form').trigger('submit'); await flushPromises(); await first.get('[data-test=credential-callback]').setValue('https://example.test/?state=private-state&code=private-code'); await first.get('form').trigger('submit'); await flushPromises()
  expect(JSON.stringify(sessionStorage)).toContain('session-a'); expect(JSON.stringify(sessionStorage)).not.toContain('private-state'); expect(JSON.stringify(sessionStorage)).not.toContain('private-code'); first.unmount()
  const second = mount(AccountCredentialDialog, { props: { account: { ...account, type: 'oauth' }, ownerId: 42 }, global: { stubs } }); await second.get('[data-test=check-credential]').trigger('click'); await flushPromises()
  expect(second.text()).toContain('marketplace.authorizationNeedsReview'); expect(api.completeAuthorization).toHaveBeenCalledTimes(1); second.unmount(); sessionStorage.clear()
})
it('does not expose a previous user credential operation after an account switch', async () => {
  sessionStorage.clear(); let resolve!: (value: unknown) => void; api.replaceCredentials.mockImplementationOnce(() => new Promise(r => { resolve = r }))
  const w = mount(AccountCredentialDialog, { props: { account: { ...account, type: 'apikey' }, ownerId: 42 }, global: { stubs } }); await w.get('[data-test=credential-key]').setValue('private-key'); await w.get('form').trigger('submit'); await flushPromises()
  await w.setProps({ ownerId: 43 }); resolve({}); await flushPromises(); expect(w.emitted('saved')).toBeUndefined(); expect(w.text()).not.toContain('private-key'); w.unmount(); sessionStorage.clear()
})
