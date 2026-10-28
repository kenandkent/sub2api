import { expect, it, vi } from 'vitest'
const { get } = vi.hoisted(() => ({ get: vi.fn() }))
vi.mock('@/api/client', () => ({ apiClient: { get } }))
import { listAccounts } from '../supplier'
it('passes all account filters and pagination to the server', async () => {
  get.mockResolvedValue({ data: { items: [] } })
  await listAccounts(2, { q: 'team', platform: 'openai', status: 'approved', model: 'gpt' })
  expect(get).toHaveBeenCalledWith('/supplier/accounts', { params: { page: 2, page_size: 20, q: 'team', platform: 'openai', status: 'approved', model: 'gpt' } })
})
