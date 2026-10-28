import { onScopeDispose, ref, watch, type Ref } from 'vue'
import { getEarnings } from '@/api/supplier'

// A response belongs to the identity and request generation that started it.
export function useSupplierHeaderWallet(userId: Ref<number | undefined>, mode: Ref<string>, route: Ref<string>) {
  const available = ref<string | null>(null)
  const loading = ref(false)
  const error = ref(false)
  let generation = 0

  async function refresh() {
    const request = ++generation
    if (!userId.value || mode.value !== 'supplier') return
    loading.value = true
    error.value = false
    try {
      const result = await getEarnings()
      if (request === generation) available.value = result.wallet.available
    } catch {
      if (request === generation) { available.value = null; error.value = true }
    } finally {
      if (request === generation) loading.value = false
    }
  }

  watch([userId, mode], () => {
    generation++
    available.value = null
    loading.value = false
    error.value = false
    void refresh()
  }, { immediate: true, flush: 'sync' })
  watch(route, () => { void refresh() })
  const onWalletChange = () => { void refresh() }
  window.addEventListener('supplier-wallet-changed', onWalletChange)
  const timer = window.setInterval(() => { if (document.visibilityState === 'visible') void refresh() }, 60_000)
  onScopeDispose(() => { generation++; window.removeEventListener('supplier-wallet-changed', onWalletChange); window.clearInterval(timer) })
  return { available, loading, error, refresh }
}
