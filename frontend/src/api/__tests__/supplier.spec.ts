import { expect, it, vi } from 'vitest'
const { get, post } = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn() }))
vi.mock('@/api/client', () => ({ apiClient: { get, post } }))
import { listAccounts } from '../supplier'
it('passes all account filters and pagination to the server', async () => {
  get.mockResolvedValue({ data: { items: [] } })
  await listAccounts(2, { q: 'team', platform: 'openai', status: 'approved', model: 'gpt' })
  expect(get).toHaveBeenCalledWith('/supplier/accounts', { params: { page: 2, page_size: 20, q: 'team', platform: 'openai', status: 'approved', model: 'gpt' } })
})

it('does not let the HTTP client automatically repeat a consumed OAuth exchange', async () => {
  post.mockResolvedValue({ data: { account_id: 1 } })
  const { completeAuthorization } = await import('../supplier')
  await completeAuthorization('session', { state: 'state', code: 'code', purpose: 'create' })
  expect(post.mock.calls[0][2]).toMatchObject({ _retry: true })
})

it('sends idempotency keys for account creation and credential replacement', async () => {
  post.mockResolvedValue({ data: {} })
  const { createAccount, replaceCredentials } = await import('../supplier')
  await createAccount({ name: 'a', platform: 'openai', type: 'api_key', api_key: 'secret', allowed_models: ['model'], concurrency: 1 }, 'key-1')
  await replaceCredentials(1, { api_key: 'secret', expected_version: 2 }, 'key-2')
  expect(post.mock.calls.at(-2)?.[2]).toEqual({ headers: { 'Idempotency-Key': 'key-1' } })
  expect(post.mock.calls.at(-1)?.[2]).toEqual({ headers: { 'Idempotency-Key': 'key-2' } })
})
