import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { useWorkModeStore } from '../workMode'

vi.mock('@/api/supplier', () => ({ getCapabilities: vi.fn() }))

describe('workspace state', () => {
  beforeEach(() => { localStorage.clear(); vi.resetAllMocks(); setActivePinia(createPinia()) })
  it('uses the route over a user preference and isolates preferences by user', () => {
    const store = useWorkModeStore()
    localStorage.setItem('auth_token', 'existing-token')
    localStorage.setItem('api-key-state', 'unchanged')
    store.setPreference(1, 'supplier')
    expect(localStorage.getItem('auth_token')).toBe('existing-token')
    expect(localStorage.getItem('api-key-state')).toBe('unchanged')
    expect(store.defaultPath(1)).toBe('/supplier/onboarding')
    expect(store.defaultPath(2)).toBe('/dashboard')
    store.recordRoute('/keys')
    expect(store.mode).toBe('consumer')
    store.recordRoute('/supplier/overview')
    expect(store.mode).toBe('supplier')
  })
  it('does not react to another tab changing the preference', () => {
    const store = useWorkModeStore()
    store.recordRoute('/keys')
    localStorage.setItem('work-mode:1', 'supplier')
    window.dispatchEvent(new StorageEvent('storage', { key: 'work-mode:1', newValue: 'supplier' }))
    expect(store.mode).toBe('consumer')
  })
  it('clears sensitive in-memory state and cancels requests on logout', async () => {
    const { getCapabilities } = await import('@/api/supplier')
    let signal: AbortSignal | undefined
    vi.mocked(getCapabilities).mockImplementation((_signal) => { signal = _signal; return new Promise(() => {}) })
    const store = useWorkModeStore()
    void store.loadCapabilities(1)
    store.clear()
    expect(signal?.aborted).toBe(true)
    expect(store.capabilities).toBeNull()
    expect(store.mode).toBe('consumer')
  })
  it('does not restore capabilities when a request resolves after logout', async () => {
    const { getCapabilities } = await import('@/api/supplier')
    let resolve!: (value: import('@/types/marketplace').UserCapabilities) => void
    vi.mocked(getCapabilities).mockReturnValue(new Promise((done) => { resolve = done }))
    const store = useWorkModeStore()
    const loading = store.loadCapabilities(1)
    store.clear()
    resolve({ consumer: true, supplier_status: 'active', marketplace_enabled: true, allowed_workspaces: ['consumer', 'supplier'] })
    await loading
    expect(store.capabilities).toBeNull()
  })
  it('fails closed for supplier access without disrupting consumer navigation', async () => {
    const { getCapabilities } = await import('@/api/supplier')
    vi.mocked(getCapabilities).mockRejectedValue(new Error('network'))
    const store = useWorkModeStore()
    await store.loadCapabilities(1)
    expect(store.targetFor('supplier')).toBe('/supplier/onboarding')
    expect(store.targetFor('consumer')).toBe('/dashboard')
    expect(store.canUseSupplier()).toBe(false)
  })
  it('routes unapproved users to onboarding and suspended users to history', () => {
    const store = useWorkModeStore()
    store.capabilities = { consumer: true, supplier_status: 'pending', marketplace_enabled: true, allowed_workspaces: ['consumer'] }
    expect(store.targetFor('supplier')).toBe('/supplier/onboarding')
    store.capabilities = { consumer: true, supplier_status: 'suspended', marketplace_enabled: true, allowed_workspaces: ['consumer', 'supplier'] }
    expect(store.targetFor('supplier')).toBe('/supplier/overview')
  })
})

describe('unsaved workspace changes', () => {
  beforeEach(() => { setActivePinia(createPinia()) })
  it('holds navigation until the user confirms or cancels', async () => {
    const store = useWorkModeStore()
    store.dirty = true
    const decision = store.confirmLeave()
    expect(store.leaveConfirmationOpen).toBe(true)
    store.resolveLeave(false)
    expect(await decision).toBe(false)
    expect(store.dirty).toBe(true)
    const next = store.confirmLeave()
    store.resolveLeave(true)
    expect(await next).toBe(true)
    expect(store.dirty).toBe(false)
  })
})
