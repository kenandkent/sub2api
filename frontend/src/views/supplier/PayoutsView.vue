<template>
  <AppLayout><div class="space-y-6">
    <div class="flex flex-wrap items-start justify-between gap-3"><div><h1 class="text-xl font-semibold">{{ t('marketplace.payouts') }}</h1><p class="mt-1 text-sm text-gray-500">{{ t('marketplace.payoutDescription') }}</p></div><button class="btn btn-secondary" :disabled="loading || busy" @click="load">{{ t('common.refresh') }}</button></div>
    <p v-if="error" role="alert" class="text-sm text-red-700">{{ error }} <button class="underline" @click="load">{{ t('marketplace.retry') }}</button></p>
    <p v-if="securityRequired" class="text-sm"><RouterLink class="text-primary-600 underline" to="/profile">{{ t('marketplace.setupSecurity') }}</RouterLink></p>
    <div v-if="loading" class="card p-6">{{ t('marketplace.loading') }}</div>
    <template v-else>
      <div v-if="wallet" class="grid gap-4 sm:grid-cols-2 xl:grid-cols-4"><StatCard :title="t('marketplace.pendingEarnings')" :value="wallet.pending" /><StatCard :title="t('marketplace.withdrawable')" :value="wallet.available" /><StatCard :title="t('marketplace.payoutReserved')" :value="wallet.payout_reserved" /><StatCard :title="t('marketplace.debtBalance')" :value="wallet.debt" /></div>
      <section class="card space-y-4 p-6"><h2 class="font-semibold">{{ t('marketplace.requestPayout') }}</h2><p class="text-sm text-gray-500">{{ t('marketplace.payoutMinimum') }}: {{ minimum || t('marketplace.priceMissing') }} USD</p>
        <p v-if="unknownKey" role="alert" class="text-sm text-amber-700">{{ t('marketplace.operationUnknown') }} <button data-test="check-operation" class="underline" :disabled="busy" @click="checkOperation">{{ t('marketplace.checkStatus') }}</button> <button v-if="pendingPayload && checkedNotFound" data-test="retry-operation" class="ml-2 underline" :disabled="busy" @click="retryOperation">{{ t('marketplace.retrySameOperation') }}</button></p>
        <div class="grid gap-4 sm:grid-cols-2"><label class="text-sm">{{ t('marketplace.destination') }}<select v-model="destination" :disabled="!!unknownKey || busy" data-test="destination" class="input mt-1"><option value="">{{ t('marketplace.selectDestination') }}</option><option v-for="d in destinations.filter(item => item.verified)" :key="d.id" :value="String(d.id)">{{ d.masked_label }}</option></select></label><label class="text-sm">{{ t('marketplace.payoutAmount') }}<input v-model.trim="amount" :disabled="!!unknownKey || busy" data-test="amount" class="input mt-1" inputmode="decimal" autocomplete="off" placeholder="0.00" /></label></div>
        <p v-if="amount && !validPayoutAmount(amount, minimum)" class="text-sm text-red-700">{{ t('marketplace.payoutAmountInvalid') }}</p><p v-else-if="amount && wallet && compareMoney(amount, wallet.available) > 0" class="text-sm text-red-700">{{ t('marketplace.payoutExceedsAvailable') }}</p><p v-if="!minimum" class="text-sm text-amber-700">{{ t('marketplace.minimumUnavailable') }}</p>
        <div class="flex flex-wrap items-center gap-3"><button data-test="request" class="btn btn-primary" :disabled="!canRequest" @click="submit">{{ t('marketplace.requestPayout') }}</button><RouterLink to="/supplier/payout-destinations" class="text-sm text-primary-600 underline">{{ t('marketplace.manageDestinations') }}</RouterLink></div>
      </section>
      <section class="card p-6"><h2 class="mb-3 font-semibold">{{ t('marketplace.payoutHistory') }}</h2><DataTable :columns="payoutColumns" :data="payouts" row-key="id"><template #empty><p class="p-6 text-sm text-gray-500">{{ t('marketplace.noPayouts') }}</p></template><template #cell-amount="{ row }"><span class="tabular-nums">{{ row.currency }} {{ row.amount }}</span></template><template #cell-status="{ row }">{{ statusLabel(row.status) }}<div v-if="row.external_reference" class="text-xs">{{ row.external_reference }}</div><div v-if="row.paid_at" class="text-xs">{{ row.paid_at }}</div></template><template #cell-actions="{ row }"><button data-test="payout-detail" class="mr-3 text-primary-600 underline" @click="openDetail(row.id)">{{ t('marketplace.details') }}</button><button v-if="row.status === 'requested'" class="text-primary-600 underline" :disabled="busy || !!unknownKey || cancelUnknown !== null" @click="cancelTarget = row">{{ t('marketplace.cancelPayout') }}</button><span v-if="cancelUnknown === row.id" class="ml-2 text-amber-700">{{ t('marketplace.operationUnknown') }}</span><button v-if="cancelUnknown === row.id" data-test="check-cancellation" class="ml-2 underline" :disabled="busy" @click="checkCancellation(row.id)">{{ t('marketplace.checkStatus') }}</button><button v-if="cancelUnknown === row.id && cancelChecked && cancelKey" data-test="retry-cancellation" class="ml-2 underline" :disabled="busy" @click="cancel(row, cancelKey)">{{ t('marketplace.retrySameOperation') }}</button></template></DataTable></section>
    </template><BaseDialog :show="!!selectedPayout" :title="t('marketplace.payoutDetails')" @close="selectedPayout = null"><dl v-if="selectedPayout" class="grid grid-cols-2 gap-4"><div><dt class="text-sm text-gray-500">{{ t('marketplace.payoutId') }}</dt><dd>{{ selectedPayout.id }}</dd></div><div><dt class="text-sm text-gray-500">{{ t('marketplace.destination') }}</dt><dd>{{ selectedPayout.masked_label }}</dd></div><div><dt class="text-sm text-gray-500">{{ t('marketplace.payoutAmount') }}</dt><dd>{{ selectedPayout.currency }} {{ selectedPayout.amount }}</dd></div><div><dt class="text-sm text-gray-500">{{ t('marketplace.status') }}</dt><dd>{{ statusLabel(selectedPayout.status) }}</dd></div><div><dt class="text-sm text-gray-500">{{ t('marketplace.paymentReference') }}</dt><dd>{{ selectedPayout.external_reference || '—' }}</dd></div><div><dt class="text-sm text-gray-500">{{ t('marketplace.paidAt') }}</dt><dd>{{ selectedPayout.paid_at || '—' }}</dd></div></dl></BaseDialog><ConfirmDialog :show="!!cancelTarget" :title="t('marketplace.cancelPayout')" :message="t('marketplace.cancelPayoutConfirm')" @cancel="cancelTarget = null" @confirm="confirmCancel" /><TotpStepUpDialog :controller="stepUp" />
  </div></AppLayout>
</template>
<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import DataTable from '@/components/common/DataTable.vue'
import StatCard from '@/components/common/StatCard.vue'
import BaseDialog from '@/components/common/BaseDialog.vue'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import TotpStepUpDialog from '@/components/auth/TotpStepUpDialog.vue'
import { useWorkModeStore } from '@/stores/workMode'
import { useAuthStore } from '@/stores/auth'
import { useStepUp, isStepUpCancelled } from '@/composables/useStepUp'
import { getEarnings, getSupplierOptions, listDestinations, listPayouts, requestPayout, cancelPayout, getPayout, getPayoutOperation } from '@/api/supplier'
import { validPayoutAmount, compareMoney } from '@/utils/money'
import type { SupplierPayout, PayoutDestination, SupplierWallet } from '@/types/marketplace'
const { t } = useI18n(); const stepUp = useStepUp(); const auth = useAuthStore(); const workMode = useWorkModeStore()
const operationStorage = `supplier-payout-operation:${auth.user?.id}`; const payloadStorage = `supplier-payout-payload:${auth.user?.id}`; const cancelStorage = `supplier-payout-cancel-id:${auth.user?.id}`; const cancelKeyStorage = `supplier-payout-cancel-key:${auth.user?.id}`
const loading = ref(true); const busy = ref(false); const error = ref(''); const securityRequired = ref(false)
const wallet = ref<SupplierWallet | null>(null); const minimum = ref(''); const destinations = ref<PayoutDestination[]>([]); const payouts = ref<SupplierPayout[]>([])
const amount = ref(''); const destination = ref(''); const unknownKey = ref(sessionStorage.getItem(operationStorage) || ''); const checkedNotFound = ref(false); const cancelUnknown = ref<number | null>(sessionStorage.getItem(cancelStorage) ? Number(sessionStorage.getItem(cancelStorage)) : null)
const payoutColumns = computed(() => [{ key: 'created_at', label: t('marketplace.date') }, { key: 'masked_label', label: t('marketplace.destination') }, { key: 'amount', label: t('marketplace.payoutAmount') }, { key: 'status', label: t('marketplace.status') }, { key: 'actions', label: t('marketplace.actions') }])
const payoutStatuses = new Set(['requested', 'approved', 'processing', 'paid', 'rejected', 'cancelled', 'failed_confirmed', 'risk_cancelled'])
function statusLabel(status: string) { return payoutStatuses.has(status) ? t(`marketplace.payoutStatus_${status}`) : status }
function readPendingPayload(): { destination_id: number; amount: string } | null {
  try { const p = JSON.parse(sessionStorage.getItem(payloadStorage) || 'null'); return p && typeof p.destination_id === 'number' && typeof p.amount === 'string' ? p : null } catch { return null }
}
const pendingPayload = ref<{ destination_id: number; amount: string } | null>(unknownKey.value ? readPendingPayload() : null)
const cancelKey = ref(sessionStorage.getItem(cancelKeyStorage) || ''); const cancelChecked = ref(false)
const selectedPayout = ref<SupplierPayout | null>(null)
async function openDetail(id: number) { try { selectedPayout.value = await getPayout(id) } catch { error.value = t('marketplace.loadFailed') } }
const cancelTarget = ref<SupplierPayout | null>(null)
watch([amount, destination], () => { workMode.dirty = !!(amount.value || destination.value) })
function beforeUnload(event: BeforeUnloadEvent) { if (workMode.dirty) { event.preventDefault(); event.returnValue = '' } }
const canRequest = computed(() => !loading.value && !busy.value && !unknownKey.value && !!destination.value && !!wallet.value && validPayoutAmount(amount.value, minimum.value) && compareMoney(amount.value, wallet.value.available) <= 0 && compareMoney(wallet.value.debt, '0') === 0)
function notifyWalletChanged() { window.dispatchEvent(new Event('supplier-wallet-changed')) }
async function load() { loading.value = true; error.value = ''; try { const [e, o, d, p] = await Promise.all([getEarnings(), getSupplierOptions(), listDestinations(), listPayouts()]); wallet.value = e.wallet; minimum.value = o.minimum_payout_usd || ''; destinations.value = d; payouts.value = p } catch { error.value = t('marketplace.loadFailed') } finally { loading.value = false } }
function handleError(e: unknown, key: string) { const err = e as { status?: number; code?: string; reason?: string }; if (isStepUpCancelled(e)) return; if ((err.code === 'STEP_UP_TOTP_NOT_ENABLED' || err.reason === 'STEP_UP_TOTP_NOT_ENABLED')) { securityRequired.value = true; return } if (!err.status || err.status >= 500 || err.status === 409) { unknownKey.value = key; checkedNotFound.value = false; sessionStorage.setItem(operationStorage, key); if (pendingPayload.value) sessionStorage.setItem(payloadStorage, JSON.stringify(pendingPayload.value)) } else error.value = t('marketplace.submitFailed') }
async function submit() { if (!canRequest.value) return; const key = crypto.randomUUID(); pendingPayload.value = { destination_id: Number(destination.value), amount: amount.value }; await sendRequest(key) }
async function sendRequest(key: string) {
  if (!pendingPayload.value || busy.value) return
  busy.value = true; const payload = pendingPayload.value; const retrying = !!unknownKey.value
  // Persist before dispatch so navigation cannot lose an in-flight operation.
  sessionStorage.setItem(operationStorage, key); sessionStorage.setItem(payloadStorage, JSON.stringify(payload))
  try {
    await stepUp.run(() => requestPayout(payload, key)); amount.value = ''; destination.value = ''; pendingPayload.value = null; unknownKey.value = ''
    sessionStorage.removeItem(operationStorage); sessionStorage.removeItem(payloadStorage); notifyWalletChanged(); await load()
  } catch (e) {
    handleError(e, key)
    if (!unknownKey.value && !retrying) { pendingPayload.value = null; sessionStorage.removeItem(operationStorage); sessionStorage.removeItem(payloadStorage) }
  } finally { busy.value = false }
}
async function retryOperation() { if (unknownKey.value && pendingPayload.value && checkedNotFound.value) await sendRequest(unknownKey.value) }
async function checkOperation() { if (!unknownKey.value || busy.value) return; busy.value = true; try { const op = await getPayoutOperation(unknownKey.value); checkedNotFound.value = op.operation_status === 'not_found'; if (op.operation_status === 'completed' && op.payout_id) { await getPayout(op.payout_id); unknownKey.value = ''; pendingPayload.value = null; sessionStorage.removeItem(operationStorage); sessionStorage.removeItem(payloadStorage); notifyWalletChanged(); await load() } } catch { error.value = t('marketplace.operationUnknown') } finally { busy.value = false } }
async function confirmCancel() { const p = cancelTarget.value; cancelTarget.value = null; if (p) await cancel(p) }
async function cancel(p: SupplierPayout, retryKey?: string) {
  if (busy.value || p.status !== 'requested' || (retryKey && !cancelChecked.value)) return
  busy.value = true; const key = retryKey || crypto.randomUUID(); cancelChecked.value = false
  sessionStorage.setItem(cancelStorage, String(p.id)); sessionStorage.setItem(cancelKeyStorage, key)
  try {
    await stepUp.run(() => cancelPayout(p.id, key))
    cancelUnknown.value = null; cancelKey.value = ''; sessionStorage.removeItem(cancelStorage); sessionStorage.removeItem(cancelKeyStorage)
    notifyWalletChanged(); await load()
  } catch (e) {
    const err = e as { status?: number; code?: string; reason?: string }
    if (err.code === 'STEP_UP_TOTP_NOT_ENABLED' || err.reason === 'STEP_UP_TOTP_NOT_ENABLED') securityRequired.value = true
    else if (!isStepUpCancelled(e) && (!err.status || err.status >= 500 || err.status === 409)) {
      cancelUnknown.value = p.id; cancelKey.value = key; sessionStorage.setItem(cancelStorage, String(p.id)); sessionStorage.setItem(cancelKeyStorage, key)
    } else {
      if (!retryKey) { sessionStorage.removeItem(cancelStorage); sessionStorage.removeItem(cancelKeyStorage) }
      if (!isStepUpCancelled(e)) error.value = t('marketplace.submitFailed')
    }
  } finally { busy.value = false }
}
async function checkCancellation(id: number) {
  if (busy.value) return
  busy.value = true
  try {
    const p = await getPayout(id); payouts.value = payouts.value.map(x => x.id === id ? p : x)
    if (p.status !== 'requested') { cancelUnknown.value = null; cancelKey.value = ''; sessionStorage.removeItem(cancelStorage); sessionStorage.removeItem(cancelKeyStorage); notifyWalletChanged() }
    else cancelChecked.value = true
  } catch { error.value = t('marketplace.operationUnknown') } finally { busy.value = false }
}
onMounted(() => { void load(); window.addEventListener('beforeunload', beforeUnload) }); onUnmounted(() => { workMode.dirty = false; window.removeEventListener('beforeunload', beforeUnload) })
</script>
