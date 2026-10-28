import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { createMemoryHistory, createRouter } from 'vue-router'
import { describe, it, expect, beforeEach, vi } from 'vitest'
import WorkModeSwitcher from '../WorkModeSwitcher.vue'
import { useWorkModeStore } from '@/stores/workMode'
import { getCapabilities } from '@/api/supplier'

vi.mock('vue-i18n', async (importOriginal) => ({ ...await importOriginal<typeof import('vue-i18n')>(), useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => ({ user: { id: 1 } }) }))
vi.mock('@/api/supplier', () => ({ getCapabilities: vi.fn() }))

describe('work mode switcher', () => {
  beforeEach(() => { localStorage.clear(); vi.resetAllMocks(); setActivePinia(createPinia()) })
  it('opens onboarding for a user without supplier access', async () => {
    const router = createRouter({ history: createMemoryHistory(), routes: [
      { path: '/dashboard', component: { template: '<div />' } },
      { path: '/supplier/onboarding', component: { template: '<div />' } },
    ] })
    await router.push('/dashboard'); await router.isReady()
    const wrapper = mount(WorkModeSwitcher, { props: { collapsed: false }, global: { plugins: [router], stubs: { ConfirmDialog: true } } })
    await wrapper.get('button').trigger('click')
    await wrapper.get('[data-mode="supplier"]').trigger('click')
    await vi.waitFor(() => expect(router.currentRoute.value.path).toBe('/supplier/onboarding'))
    expect(localStorage.getItem('work-mode:1')).toBe('supplier')
    expect(useWorkModeStore().mode).toBe('consumer') // only the router updates the current mode
  })
  it('saves an explicit selection even when already at the target route', async () => {
    const router = createRouter({ history: createMemoryHistory(), routes: [
      { path: '/dashboard', component: { template: '<div />' } },
    ] })
    await router.push('/dashboard'); await router.isReady()
    const wrapper = mount(WorkModeSwitcher, { props: { collapsed: false }, global: { plugins: [router] } })
    await wrapper.get('button').trigger('click')
    await wrapper.get('[data-mode="consumer"]').trigger('click')
    await vi.waitFor(() => expect(localStorage.getItem('work-mode:1')).toBe('consumer'))
  })

  it('falls back when the remembered supplier route no longer exists', async () => {
    const router = createRouter({ history: createMemoryHistory(), routes: [
      { path: '/dashboard', component: { template: '<div />' } },
      { path: '/supplier/overview', component: { template: '<div />' } },
      { path: '/:pathMatch(.*)*', name: 'NotFound', component: { template: '<div />' } },
    ] })
    vi.mocked(getCapabilities).mockResolvedValue({ consumer: true, supplier_status: 'active', marketplace_enabled: true, allowed_workspaces: ['consumer', 'supplier'] })
    const store = useWorkModeStore()
    store.recordRoute('/supplier/removed')
    store.recordRoute('/dashboard')
    await router.push('/dashboard'); await router.isReady()
    const wrapper = mount(WorkModeSwitcher, { props: { collapsed: false }, global: { plugins: [router] } })
    await wrapper.get('button').trigger('click')
    await wrapper.get('[data-mode="supplier"]').trigger('click')
    await vi.waitFor(() => expect(router.currentRoute.value.path).toBe('/supplier/overview'))
  })
  it('does not save a selection when navigation is cancelled', async () => {
    const router = createRouter({ history: createMemoryHistory(), routes: [
      { path: '/dashboard', component: { template: '<div />' } },
      { path: '/supplier/onboarding', component: { template: '<div />' } },
    ] })
    await router.push('/dashboard'); await router.isReady()
    const guard = vi.fn(() => false)
    router.beforeEach(guard)
    const wrapper = mount(WorkModeSwitcher, { props: { collapsed: false }, global: { plugins: [router] } })
    await wrapper.get('button').trigger('click')
    await wrapper.get('[data-mode="supplier"]').trigger('click')
    await vi.waitFor(() => expect(guard).toHaveBeenCalled())
    await flushPromises()
    expect(router.currentRoute.value.path).toBe('/dashboard')
    expect(localStorage.getItem('work-mode:1')).toBeNull()
  })

})
