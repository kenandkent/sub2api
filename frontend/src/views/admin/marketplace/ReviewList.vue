<template>
  <AppLayout><TablePageLayout>
    <template #filters><div class="flex flex-wrap items-center justify-between gap-4"><div><h1 class="text-2xl font-semibold">{{ t(`adminMarketplace.${kind}`) }}</h1><p class="mt-1 text-sm text-gray-500">{{ t('adminMarketplace.reviewHint') }}</p></div><button class="btn btn-secondary" :disabled="loading" @click="load">{{ t('common.refresh') }}</button></div><p v-if="error" role="alert" class="mt-3 text-red-600">{{ error }} <button class="underline" @click="load">{{ t('adminMarketplace.retry') }}</button></p></template>
    <template #table><DataTable :columns="columns" :data="rows" :loading="loading"><template #cell-details="{ row }"><span v-if="kind === 'suppliers'">{{ Object.entries(row.application_fields || {}).map(([k,v]) => `${k}: ${v}`).join(', ') }} · {{ t('adminMarketplace.agreement') }} {{ row.agreement_version }} · {{ row.payout_frozen ? t('adminMarketplace.frozen') : t('adminMarketplace.notFrozen') }}<span v-if="row.review_reason"> · {{ row.review_reason }}</span></span><span v-else>{{ row.platform }} / {{ row.type }} · {{ row.concurrency }} · {{ (row.allowed_models || []).join(', ') }} · {{ row.enabled ? t('adminMarketplace.enabled') : t('adminMarketplace.disabled') }} · {{ t('marketplace.credentialFingerprint') }}: {{ row.credential_fingerprint_present ? t('marketplace.present') : t('marketplace.absent') }}</span></template><template #cell-actions="{ row }"><button class="btn btn-secondary btn-sm" @click="open(row)">{{ t('adminMarketplace.review') }}</button><button v-if="kind === 'suppliers'" data-test="payout-freeze" class="btn btn-secondary btn-sm ml-2" @click="openFreeze(row)">{{ t(row.payout_frozen ? 'adminMarketplace.unfreezePayouts' : 'adminMarketplace.freezePayouts') }}</button></template></DataTable></template>
    <template #pagination><Pagination v-if="total > 20" :page="page" :page-size="20" :total="total" @update:page="changePage" /></template>
  </TablePageLayout>
  <BaseDialog :show="!!selected" :title="t('adminMarketplace.review')" @close="selected = null"><div class="space-y-4"><p>{{ t('adminMarketplace.version') }} {{ selected?.version }} · #{{ kind === 'suppliers' ? selected?.user_id : selected?.id }}</p><label class="block">{{ t('adminMarketplace.decision') }}<select v-model="decision" class="input mt-1"><option v-for="choice in choices" :key="choice" :value="choice">{{ t(`adminMarketplace.${choice}`) }}</option></select></label><label class="block">{{ t('adminMarketplace.reason') }}<textarea v-model="reason" class="input mt-1" rows="3" required /></label><label v-if="kind === 'offers' && decision === 'approved'" class="flex items-start gap-2"><input v-model="identityReviewed" data-test="identity-reviewed" type="checkbox" />{{ t('adminMarketplace.identityReviewed') }}</label><p v-if="dialogError" role="alert" class="text-red-600">{{ dialogError }} <button v-if="conflict" class="underline" @click="reloadSelected">{{ t('adminMarketplace.reloadLatest') }}</button></p></div><template #footer><button class="btn btn-secondary" @click="selected = null">{{ t('common.cancel') }}</button><button class="btn btn-primary" :disabled="busy || !reason.trim() || (kind === 'offers' && decision === 'approved' && !identityReviewed)" @click="submit">{{ t('adminMarketplace.confirmReview') }}</button></template></BaseDialog>
  <BaseDialog :show="!!freezeTarget" :title="t(freezeTarget?.payout_frozen ? 'adminMarketplace.unfreezePayouts' : 'adminMarketplace.freezePayouts')" @close="closeFreeze"><div class="space-y-4"><p>{{ t('adminMarketplace.freezeHint') }}</p><label class="block">{{ t('adminMarketplace.reason') }}<textarea v-model="freezeReason" data-test="freeze-reason" class="input mt-1" rows="3" :disabled="!!freezeOperation" /></label><p v-if="freezeOperation" class="text-sm text-amber-700">{{ t('adminMarketplace.retrySame') }}</p><p v-if="freezeError" role="alert" class="text-red-600">{{ freezeError }}</p></div><template #footer><button class="btn btn-secondary" :disabled="busy" @click="closeFreeze">{{ t('common.cancel') }}</button><button data-test="submit-freeze" class="btn btn-primary" :disabled="busy || !freezeReason.trim()" @click="submitFreeze">{{ freezeOperation ? t('adminMarketplace.retryAction') : t('adminMarketplace.confirmAction') }}</button></template></BaseDialog>
  <TotpStepUpDialog :controller="stepUp" />
  </AppLayout>
</template>
<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useAuthStore } from '@/stores/auth'
import TotpStepUpDialog from '@/components/auth/TotpStepUpDialog.vue'
import { useStepUp, isStepUpCancelled } from '@/composables/useStepUp'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import TablePageLayout from '@/components/layout/TablePageLayout.vue'
import DataTable from '@/components/common/DataTable.vue'
import Pagination from '@/components/common/Pagination.vue'
import BaseDialog from '@/components/common/BaseDialog.vue'
import { items, list, post, type Supplier, type Offer } from '@/api/admin/marketplace'
const props = defineProps<{ kind: 'suppliers' | 'offers' }>()
const { t } = useI18n()
const auth = useAuthStore(), stepUp = useStepUp()
const loading = ref(false), busy = ref(false), conflict = ref(false), error = ref(''), dialogError = ref(''), page = ref(1), total = ref(0)
const identityReviewed = ref(false)
const rows = ref<(Supplier | Offer)[]>([]), selected = ref<any>(null), decision = ref(''), reason = ref('')
const choices = computed(() => props.kind === 'suppliers' ? ['active', 'rejected', 'suspended'] : ['approved', 'rejected'])
const columns = computed(() => [{ key: props.kind === 'suppliers' ? 'user_id' : 'id', label: t('adminMarketplace.id') }, { key: props.kind === 'suppliers' ? 'status' : 'review_status', label: t('adminMarketplace.status'), formatter: (value: unknown) => t(props.kind === 'suppliers' ? `marketplace.${String(value)}` : `marketplace.review_${String(value)}`) }, ...(props.kind === 'offers' ? [{ key: 'account_name', label: t('adminMarketplace.account') }, { key: 'owner_user_id', label: t('adminMarketplace.supplier') }] : []), { key: 'details', label: t('adminMarketplace.details') }, { key: 'actions', label: t('adminMarketplace.actions') }])
async function load() { loading.value = true; error.value = ''; try { const result = await list<Supplier | Offer>(props.kind, page.value); rows.value = items(result); total.value = Array.isArray(result) ? result.length : result.total } catch (e: any) { error.value = e?.message || t('adminMarketplace.loadError') } finally { loading.value = false } }
function changePage(next: number) { page.value = next; void load() }
async function reloadSelected() { const id = props.kind === 'suppliers' ? selected.value?.user_id : selected.value?.id; await load(); const latest = rows.value.find((row: any) => (props.kind === 'suppliers' ? row.user_id : row.id) === id); if (latest) { selected.value = latest; dialogError.value = ''; conflict.value = false } else dialogError.value = error.value || t('adminMarketplace.loadError') }
function open(row: Supplier | Offer) { identityReviewed.value = false; conflict.value = false; selected.value = row; decision.value = choices.value[0]; reason.value = ''; dialogError.value = '' }
async function submit() { if (!selected.value || !reason.value.trim() || busy.value || (props.kind === 'offers' && decision.value === 'approved' && !identityReviewed.value)) return; busy.value = true; dialogError.value = ''; try { const id = props.kind === 'suppliers' ? selected.value.user_id : selected.value.id; await post(`${props.kind}/${id}/review`, { status: decision.value, reason: reason.value, expected_version: selected.value.version, ...(props.kind === 'offers' && decision.value === 'approved' ? { identity_reviewed: true } : {}) }); selected.value = null; await load() } catch (e: any) { conflict.value = e?.status === 409; dialogError.value = conflict.value ? t('adminMarketplace.versionConflict') : e?.message || t('adminMarketplace.actionError') } finally { busy.value = false } }
type FreezeOperation = { key: string; body: { expected_version: number; frozen: boolean; reason: string } }
const freezeTarget = ref<Supplier | null>(null), freezeReason = ref(''), freezeError = ref(''), freezeOperation = ref<FreezeOperation | null>(null)
const freezeOperations = new Map<number, FreezeOperation>()
function freezeStorage(id: number) { return `admin-payout-freeze:${auth.user?.id}:${id}` }
function savedFreeze(id: number): FreezeOperation | null {
  try {
    const op = JSON.parse(sessionStorage.getItem(freezeStorage(id)) || 'null')
    return op && typeof op.key === 'string' && typeof op.body?.expected_version === 'number' && typeof op.body?.frozen === 'boolean' && typeof op.body?.reason === 'string' ? op : null
  } catch { return null }
}
function forgetFreeze(id: number) { freezeOperations.delete(id); sessionStorage.removeItem(freezeStorage(id)) }
let freezeGeneration = 0
function openFreeze(row: Supplier) {
  freezeGeneration++; freezeTarget.value = row; freezeError.value = ''
  freezeOperation.value = freezeOperations.get(row.user_id) || savedFreeze(row.user_id)
  freezeReason.value = freezeOperation.value?.body.reason || ''
}
function closeFreeze() { freezeGeneration++; freezeTarget.value = null; freezeError.value = '' }
async function submitFreeze() {
  if (!freezeTarget.value || !freezeReason.value.trim() || busy.value || !auth.user) return
  const target = freezeTarget.value, actor = auth.user.id, generation = freezeGeneration
  const operation = freezeOperation.value || { key: crypto.randomUUID(), body: { expected_version: target.version, frozen: !target.payout_frozen, reason: freezeReason.value.trim() } }
  freezeOperations.set(target.user_id, operation); sessionStorage.setItem(freezeStorage(target.user_id), JSON.stringify(operation)); freezeOperation.value = operation; busy.value = true; freezeError.value = ''
  const active = () => generation === freezeGeneration && auth.user?.id === actor
  try {
    await stepUp.run(() => active() ? post(`suppliers/${target.user_id}/payout-freeze`, operation.body, operation.key) : Promise.reject(new Error('Cancelled')))
    if (active() && freezeOperations.get(target.user_id) === operation) forgetFreeze(target.user_id)
    if (active()) { closeFreeze(); await load() }
  } catch (e: any) {
    if (!active()) return
    if (isStepUpCancelled(e)) { forgetFreeze(target.user_id); freezeOperation.value = null }
    else if (e?.status && e.status < 500 && e.status !== 408 && e.status !== 429) {
      forgetFreeze(target.user_id); freezeOperation.value = null
      freezeError.value = e?.message || t('adminMarketplace.actionError')
      await load()
      if (active()) freezeTarget.value = rows.value.find((row): row is Supplier => 'user_id' in row && row.user_id === target.user_id) || target
    } else freezeError.value = e?.message || t('adminMarketplace.actionError')
  } finally { busy.value = false }
}
watch(() => auth.user?.id, () => { closeFreeze(); freezeOperations.clear() }, { flush: 'sync' })
onUnmounted(() => { closeFreeze(); freezeOperations.clear() })
onMounted(load)
</script>
