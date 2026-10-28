import { defineStore } from 'pinia'
import { ref } from 'vue'
import { getCapabilities } from '@/api/supplier'
import type { UserCapabilities, WorkMode } from '@/types/marketplace'

export const useWorkModeStore = defineStore('workMode', () => {
  const mode = ref<WorkMode>('consumer')
  const capabilities = ref<UserCapabilities | null>(null)
  const dirty = ref(false)
  const leaveConfirmationOpen = ref(false)
  let resolvePending: ((confirmed: boolean) => void) | null = null
  let pendingLeave: Promise<boolean> | null = null
  const lastConsumer = ref('/dashboard')
  const lastSupplier = ref('/supplier/overview')
  let request: AbortController | null = null
  let capabilityUser: number | null = null
  let generation = 0

  function setPreference(userId: number, value: WorkMode) {
    localStorage.setItem(`work-mode:${userId}`, value)
  }
  function canUseSupplier() {
    return !!capabilities.value?.marketplace_enabled &&
      capabilities.value.allowed_workspaces.includes('supplier') &&
      ['active', 'suspended'].includes(capabilities.value.supplier_status)
  }
  function targetFor(value: WorkMode) {
    if (value === 'consumer') return lastConsumer.value
    return canUseSupplier() ? lastSupplier.value : '/supplier/onboarding'
  }
  function defaultPath(userId: number) {
    return targetFor(localStorage.getItem(`work-mode:${userId}`) === 'supplier' ? 'supplier' : 'consumer')
  }
  function recordRoute(path: string, matched = true) {
    if (path === '/supplier' || path.startsWith('/supplier/')) {
      mode.value = 'supplier'
      if (matched && path !== '/supplier/onboarding') lastSupplier.value = path
    } else if (path !== '/admin' && !path.startsWith('/admin/')) {
      mode.value = 'consumer'
      if (matched) lastConsumer.value = path
    }
  }
  async function loadCapabilities(userId: number) {
    if (capabilityUser !== userId) {
      request?.abort()
      capabilities.value = null
      capabilityUser = userId
    }
    request?.abort()
    const controller = new AbortController()
    request = controller
    const sequence = ++generation
    try {
      const result = await getCapabilities(controller.signal)
      if (sequence === generation) capabilities.value = result
    } catch {
      if (sequence === generation) capabilities.value = null
    } finally {
      if (request === controller) request = null
    }
  }
  function confirmLeave(): Promise<boolean> {
    if (!dirty.value) return Promise.resolve(true)
    if (pendingLeave) return pendingLeave
    leaveConfirmationOpen.value = true
    pendingLeave = new Promise<boolean>((resolve) => { resolvePending = resolve })
    return pendingLeave
  }
  function resolveLeave(confirmed: boolean) {
    leaveConfirmationOpen.value = false
    if (confirmed) dirty.value = false
    resolvePending?.(confirmed)
    resolvePending = null
    pendingLeave = null
  }
  function clear() {
    resolveLeave(false)
    generation++
    request?.abort()
    request = null
    capabilityUser = null
    capabilities.value = null
    mode.value = 'consumer'
    dirty.value = false
    lastConsumer.value = '/dashboard'
    lastSupplier.value = '/supplier/overview'
  }
  return { mode, capabilities, dirty, leaveConfirmationOpen, confirmLeave, resolveLeave, setPreference, canUseSupplier, targetFor, defaultPath, recordRoute, loadCapabilities, clear }
})
