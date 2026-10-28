import { effectScope, nextTick, ref } from 'vue'
import { flushPromises } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { useSupplierHeaderWallet } from '../useSupplierHeaderWallet'

const api = vi.hoisted(() => ({ getEarnings: vi.fn() }))
vi.mock('@/api/supplier', () => api)
const deferred = () => { let resolve!: (value: any) => void; let reject!: (error: Error) => void; const promise = new Promise<any>((a, b) => { resolve = a; reject = b }); return { promise, resolve, reject } }
const result = (available: string) => ({ wallet: { available } })
let stop: (() => void) | undefined
afterEach(() => { stop?.(); stop = undefined; vi.clearAllMocks() })
function setup() {
  const userId = ref<number | undefined>(1), mode = ref('supplier'), route = ref('/supplier/overview')
  const scope = effectScope(); const state = scope.run(() => useSupplierHeaderWallet(userId, mode, route))!
  stop = () => scope.stop()
  return { userId, mode, route, ...state }
}
describe('supplier header wallet', () => {
  it('loads exact string values and refreshes on route and payout changes', async () => {
    api.getEarnings.mockResolvedValueOnce(result('12345678901234567890.12345678')).mockResolvedValueOnce(result('2.00')).mockResolvedValueOnce(result('3.00'))
    const s = setup(); expect(s.loading.value).toBe(true); await flushPromises()
    expect(s.available.value).toBe('12345678901234567890.12345678')
    s.route.value = '/supplier/payouts'; await nextTick(); await flushPromises(); expect(s.available.value).toBe('2.00')
    window.dispatchEvent(new Event('supplier-wallet-changed')); await flushPromises(); expect(s.available.value).toBe('3.00')
  })
  it('clears errors and retries without showing an old balance', async () => {
    api.getEarnings.mockRejectedValueOnce(new Error('offline')).mockResolvedValueOnce(result('5.00'))
    const s = setup(); await flushPromises(); expect(s.error.value).toBe(true); expect(s.available.value).toBeNull()
    await s.refresh(); expect(s.error.value).toBe(false); expect(s.available.value).toBe('5.00')
  })
  it('clears immediately and ignores late responses after user or mode changes', async () => {
    const old = deferred(), current = deferred(); api.getEarnings.mockReturnValueOnce(old.promise).mockReturnValueOnce(current.promise)
    const s = setup(); s.userId.value = 2; expect(s.available.value).toBeNull(); old.resolve(result('1')); await flushPromises(); expect(s.available.value).toBeNull()
    current.resolve(result('2')); await flushPromises(); expect(s.available.value).toBe('2')
    s.mode.value = 'consumer'; expect(s.available.value).toBeNull(); s.userId.value = undefined; await nextTick()
  })
})
