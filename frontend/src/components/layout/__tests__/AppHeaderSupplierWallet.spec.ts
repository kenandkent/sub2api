import { flushPromises, mount } from '@vue/test-utils'
import { reactive } from 'vue'
import { describe, expect, it, vi } from 'vitest'
import AppHeader from '../AppHeader.vue'
const state = vi.hoisted(() => ({ getEarnings: vi.fn() }))
vi.mock('@/api/supplier', () => state)
vi.mock('vue-router', () => ({ useRouter: () => ({ push: vi.fn() }), useRoute: () => ({ fullPath: '/supplier', meta: {}, params: {} }) }))
vi.mock('vue-i18n', async () => ({ ...(await vi.importActual<typeof import('vue-i18n')>('vue-i18n')), useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@/stores', () => ({ useAppStore: () => ({ docUrl: '' }), useAuthStore: () => reactive({ user: { id: 1, email: 'supplier@example.com', role: 'user' } }), useOnboardingStore: () => ({}) }))
vi.mock('@/stores/adminSettings', () => ({ useAdminSettingsStore: () => ({ customMenuItems: [] }) }))
vi.mock('@/stores/workMode', () => ({ useWorkModeStore: () => ({ mode: 'supplier' }) }))
vi.mock('@/utils/featureFlags', () => ({ FeatureFlags: {}, isFeatureFlagEnabled: () => false }))
vi.mock('@/router/title', () => ({ resolveRouteMetaKeys: () => ({}) }))
vi.mock('@/utils/siteBillingMode', () => ({ resolveSiteBillingMode: () => 'standard' }))
describe('supplier wallet in header', () => {
  it('shows errors with a retry and displays the exact value in desktop and mobile', async () => {
    state.getEarnings.mockRejectedValueOnce(new Error('offline')).mockResolvedValueOnce({ wallet: { available: '999999999999999.12345678' } })
    const w = mount(AppHeader, { global: { stubs: { Icon: true, AnnouncementBell: true, LocaleSwitcher: true, SubscriptionProgressMini: true, RouterLink: true } } }); await flushPromises()
    expect(w.text()).toContain('marketplace.loadFailed')
    await w.findAll('button').find(b => b.text() === 'marketplace.retry')!.trigger('click'); await flushPromises()
    await w.get('[aria-label="common.userMenu"]').trigger('click')
    expect(w.text().match(/999999999999999\.12345678/g)).toHaveLength(2)
    w.unmount()
  })
})
