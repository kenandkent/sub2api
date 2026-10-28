<template><AppLayout><div class="space-y-6">
<header class="flex flex-wrap items-center justify-between gap-4"><div><h1 class="text-2xl font-semibold">{{ t('adminMarketplace.payouts') }}</h1><p class="mt-1 text-sm text-gray-500">{{ t('adminMarketplace.payoutHint') }}</p></div><button class="btn btn-secondary" :disabled="loading" @click="load">{{ t('common.refresh') }}</button></header><p v-if="error" role="alert" class="text-red-600">{{ error }}</p>
<section class="card overflow-hidden"><DataTable :columns="columns" :data="payouts" :loading="loading"><template #cell-actions="{ row }"><button class="btn btn-secondary btn-sm" @click="openPayout(row)">{{ t('adminMarketplace.details') }}</button></template></DataTable><Pagination v-if="payoutTotal > 20" :page="payoutPage" :page-size="20" :total="payoutTotal" @update:page="payoutPage = $event; load()" /></section>
<section class="space-y-3"><h2 class="text-lg font-semibold">{{ t('adminMarketplace.destinations') }}</h2><div class="card overflow-hidden"><DataTable :columns="destinationColumns" :data="destinations" :loading="loading"><template #cell-actions="{ row }"><div class="flex flex-wrap gap-2"><button class="btn btn-secondary btn-sm" @click="openDestination(row, 'reveal')">{{ t('adminMarketplace.reveal') }}</button><button class="btn btn-secondary btn-sm" @click="openDestination(row, 'verify')">{{ t('adminMarketplace.verify') }}</button></div></template></DataTable><Pagination v-if="destinationTotal > 20" :page="destinationPage" :page-size="20" :total="destinationTotal" @update:page="destinationPage = $event; load()" /></div></section>
<BaseDialog :show="!!selected" :title="t('adminMarketplace.payoutDetails')" @close="closePayout"><div v-if="selected" class="space-y-4"><dl class="grid grid-cols-2 gap-3 text-sm"><template v-for="field in payoutFields" :key="field"><dt class="text-gray-500">{{ t(`adminMarketplace.${field}`) }}</dt><dd class="break-words">{{ selected[field] || '—' }}</dd></template></dl><p v-if="selected.status === 'processing'" class="rounded-lg bg-amber-50 p-3 text-sm text-amber-900 dark:bg-amber-900/20 dark:text-amber-200">{{ t('adminMarketplace.processingWarning') }}</p><p v-if="!canApprove(auth.user?.id, selected.supplier_id)" class="text-sm text-gray-500">{{ t('adminMarketplace.selfApproval') }}</p><div class="flex flex-wrap gap-2"><button v-for="action in availableActions(selected)" :key="action" class="btn btn-secondary" :disabled="detailLoading || detailFailed || (!canApprove(auth.user?.id, selected.supplier_id) && (action === 'approve' || action === 'confirm')) || hasOtherPending(selected.id, action)" @click="startAction(action)">{{ t(`adminMarketplace.${action}`) }}</button></div></div><p v-if="detailLoading">{{ t('common.loading') }}</p><p v-if="dialogError" role="alert" class="mt-3 text-red-600">{{ dialogError }}</p></BaseDialog>
<BaseDialog :show="!!action" :title="t(`adminMarketplace.${action || 'confirm'}`)" @close="closeAction"><div class="space-y-4"><p>{{ t('adminMarketplace.actionConfirm') }}</p><p v-if="attempted" class="text-sm text-amber-700 dark:text-amber-300">{{ t('adminMarketplace.retrySame') }} <span v-if="selected && operations.get(`payout:${selected.id}:${action}`)" class="break-all">{{ t('marketplace.operationReference') }}: {{ operations.get(`payout:${selected.id}:${action}`)?.key }}</span> <button class="underline" :disabled="busy" @click="checkStatus">{{ t('adminMarketplace.checkStatus') }}</button></p><template v-if="action === 'confirm'"><label class="block">{{ t('adminMarketplace.external_reference') }}<input v-model="form.external_reference" class="input mt-1" :disabled="attempted && !payoutReentry" /></label><label class="block">{{ t('adminMarketplace.amount') }} (USD)<input v-model="form.amount" class="input mt-1" inputmode="decimal" :disabled="attempted && !payoutReentry" /></label><label class="block">{{ t('adminMarketplace.paid_at') }}<input v-model="form.paid_at" class="input mt-1" type="datetime-local" :disabled="attempted && !payoutReentry" /></label></template><label v-if="['confirm', 'reject', 'fail'].includes(action)" class="block">{{ t('adminMarketplace.evidence') }}<textarea v-model="form.evidence" class="input mt-1" rows="3" :disabled="attempted && !payoutReentry" /></label><p v-if="dialogError" role="alert" class="text-red-600">{{ dialogError }}</p></div><template #footer><button class="btn btn-secondary" :disabled="busy" @click="closeAction">{{ t('common.cancel') }}</button><button class="btn btn-primary" :disabled="busy || !validAction || (attempted && !statusChecked)" @click="submitAction">{{ attempted ? t('adminMarketplace.retryAction') : t('adminMarketplace.confirmAction') }}</button></template></BaseDialog>
<BaseDialog :show="!!destination" :title="t(`adminMarketplace.${destinationAction || 'reveal'}`)" @close="closeDestination"><div class="space-y-4"><p>{{ t(destinationAction === 'reveal' ? 'adminMarketplace.revealHint' : 'adminMarketplace.verifyHint') }}</p><label v-if="!secret">{{ t(destinationAction === 'reveal' ? 'adminMarketplace.reason' : 'adminMarketplace.evidence') }}<textarea v-model="destinationInput" class="input mt-1" rows="3" :disabled="destinationAttempted && !destinationReentry" /></label><div v-if="secret" class="break-all rounded-lg bg-gray-100 p-3 font-mono dark:bg-dark-900">{{ secret }}</div><p v-if="destinationAttempted && destination" class="break-all text-sm">{{ t('marketplace.operationReference') }}: {{ operations.get(`destination:${destination.id}:${destinationAction}`)?.key }}</p><p v-if="dialogError" role="alert" class="text-red-600">{{ dialogError }}</p></div><template #footer><button class="btn btn-secondary" @click="closeDestination">{{ t('common.close') }}</button><button v-if="!secret" class="btn btn-primary" :disabled="busy || !destinationInput.trim()" @click="submitDestination">{{ destinationAttempted ? t('adminMarketplace.retryAction') : t('adminMarketplace.confirmAction') }}</button></template></BaseDialog><TotpStepUpDialog :controller="stepUp" />
</div></AppLayout></template>
<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAuthStore } from '@/stores/auth'
import AppLayout from '@/components/layout/AppLayout.vue'
import DataTable from '@/components/common/DataTable.vue'
import Pagination from '@/components/common/Pagination.vue'
import BaseDialog from '@/components/common/BaseDialog.vue'
import TotpStepUpDialog from '@/components/auth/TotpStepUpDialog.vue'
import { useStepUp, isStepUpCancelled } from '@/composables/useStepUp'
import { get, items, list, post, type Payout, type Destination } from '@/api/admin/marketplace'
import { canApprove, payoutActions, sameMoney } from './adminState'
import { formatDateTimeLocalInput } from '@/utils/format'
const { t } = useI18n(), auth = useAuthStore(), stepUp = useStepUp()
const loading = ref(false), detailLoading = ref(false), detailFailed = ref(false), statusChecked = ref(false), busy = ref(false), error = ref(''), dialogError = ref('')
const payoutPage = ref(1), payoutTotal = ref(0), destinationPage = ref(1), destinationTotal = ref(0)
const payouts = ref<Payout[]>([]), destinations = ref<Destination[]>([]), selected = ref<Payout | null>(null), action = ref(''), attempted = ref(false)
const form = ref({ external_reference: '', amount: '', paid_at: '', evidence: '' })
const destination = ref<Destination | null>(null), destinationAction = ref(''), destinationInput = ref(''), destinationAttempted = ref(false), secret = ref('')
// Keep the same key and payload for an uncertain request, even if its dialog is reopened.
let destinationGeneration = 0
let payoutGeneration = 0
let listGeneration = 0
let secretTimer: ReturnType<typeof setTimeout> | undefined
function clearSecret() { secret.value = ''; if (secretTimer) clearTimeout(secretTimer); secretTimer = undefined }
type Operation = { key: string; salt: string; digest: string; body?: unknown }
const operations = new Map<string, Operation>()
const storagePrefix = (actor: number) => `marketplace-admin-operation:${actor}:`
function storageKey(actor: number, id: string) { return storagePrefix(actor) + id }
function restoreOperations(actor: number) { operations.clear(); const prefix = storagePrefix(actor); for (let i = 0; i < sessionStorage.length; i++) { const key = sessionStorage.key(i); if (!key?.startsWith(prefix)) continue; const id = key.slice(prefix.length); if (!/^(?:payout|destination):[1-9]\d*:(?:approve|reject|start|confirm|fail|reveal|verify)$/.test(id)) continue; try { const op = JSON.parse(sessionStorage.getItem(key) || 'null'); if (typeof op?.key === 'string' && typeof op.salt === 'string' && typeof op.digest === 'string') operations.set(id, op) } catch { /* Ignore invalid metadata. */ } } }
async function fingerprint(body: unknown, salt: string) { const bytes = new TextEncoder().encode(`${salt}\0${JSON.stringify(body)}`); const hash = await crypto.subtle.digest('SHA-256', bytes); return Array.from(new Uint8Array(hash), x => x.toString(16).padStart(2, '0')).join('') }
function saveOperation(actor: number, id: string, op: Operation) { sessionStorage.setItem(storageKey(actor, id), JSON.stringify({ key: op.key, salt: op.salt, digest: op.digest })) }
function forgetOperation(actor: number, id: string, op: Operation) { if (operations.get(id) === op) operations.delete(id); try { const saved = JSON.parse(sessionStorage.getItem(storageKey(actor, id)) || 'null'); if (saved?.key === op.key) sessionStorage.removeItem(storageKey(actor, id)) } catch { /* Leave unreadable metadata untouched. */ } }
const payoutReentry = computed(() => !!selected.value && attempted.value && operations.get(`payout:${selected.value.id}:${action.value}`)?.body === undefined)
const destinationReentry = computed(() => !!destination.value && destinationAttempted.value && operations.get(`destination:${destination.value.id}:${destinationAction.value}`)?.body === undefined)
function availableActions(row: Payout) { const prefix = `payout:${row.id}:`; return [...new Set([...payoutActions(row.status), ...[...operations.keys()].filter(k => k.startsWith(prefix)).map(k => k.slice(prefix.length))])] }
const columns = computed(() => ['id', 'supplier_id', 'status', 'amount', 'currency', 'created_at', 'actions'].map(key => ({ key, label: t(`adminMarketplace.${key}`) })))
const destinationColumns = computed(() => ['id', 'supplier_id', 'masked_label', 'verified', 'verified_by', 'verified_at', 'verification_evidence', 'actions'].map(key => ({ key, label: t(`adminMarketplace.${key}`) })))
const payoutFields = ['id', 'supplier_id', 'status', 'amount', 'currency', 'external_reference', 'paid_at', 'evidence'] as const
const validAction = computed(() => action.value === 'confirm' ? !!selected.value && selected.value.currency === 'USD' && sameMoney(form.value.amount, selected.value.amount) && !!form.value.external_reference.trim() && !!form.value.evidence.trim() && !!form.value.paid_at : ['reject', 'fail'].includes(action.value) ? !!form.value.evidence.trim() : true)
async function load() { const generation = ++listGeneration, actor = auth.user?.id, pPage = payoutPage.value, dPage = destinationPage.value; if (!actor) return; loading.value = true; error.value = ''; try { const [p, d] = await Promise.all([list<Payout>('payouts', pPage), list<Destination>('payout-destinations', dPage)]); if (generation !== listGeneration || auth.user?.id !== actor) return; payouts.value = Array.isArray(p) ? p.slice((pPage - 1) * 20, pPage * 20) : items(p); destinations.value = Array.isArray(d) ? d.slice((dPage - 1) * 20, dPage * 20) : items(d); payoutTotal.value = Array.isArray(p) ? p.length : p.total; destinationTotal.value = Array.isArray(d) ? d.length : d.total } catch (e: any) { if (generation === listGeneration && auth.user?.id === actor) error.value = e?.message || t('adminMarketplace.loadError') } finally { if (generation === listGeneration && auth.user?.id === actor) loading.value = false } }
async function openPayout(row: Payout) { const generation = ++payoutGeneration; selected.value = row; dialogError.value = ''; detailLoading.value = true; detailFailed.value = false; try { const result = await get<Payout>(`payouts/${row.id}`); if (generation === payoutGeneration) selected.value = result } catch (e: any) { if (generation === payoutGeneration) { detailFailed.value = true; dialogError.value = e?.message || t('adminMarketplace.loadError') } } finally { if (generation === payoutGeneration) detailLoading.value = false } }
function closePayout() { payoutGeneration++; selected.value = null; dialogError.value = '' }
function hasOtherPending(id: number, next: string) { return [...operations.keys()].some(key => key.startsWith(`payout:${id}:`) && key !== `payout:${id}:${next}`) }
async function checkStatus() { if (!selected.value || !auth.user) return; const id = selected.value.id, actor = auth.user.id, generation = payoutGeneration; busy.value = true; dialogError.value = ''; try { const fresh = await get<Payout>(`payouts/${id}`); if (auth.user?.id === actor && payoutGeneration === generation) { selected.value = fresh; statusChecked.value = true } } catch (e: any) { if (auth.user?.id === actor && payoutGeneration === generation) dialogError.value = e?.message || t('adminMarketplace.loadError') } finally { busy.value = false } }
function startAction(next: string) { if (!selected.value) return; payoutGeneration++; action.value = next; dialogError.value = ''; const saved = operations.get(`payout:${selected.value.id}:${next}`); attempted.value = !!saved; statusChecked.value = false; const body = saved?.body as any; form.value = { external_reference: body?.external_reference || '', amount: body?.amount || '', paid_at: body?.paid_at ? formatDateTimeLocalInput(Math.floor(new Date(body.paid_at).getTime() / 1000)) : '', evidence: body?.evidence || '' } }
function closeAction() { payoutGeneration++; action.value = ''; dialogError.value = '' }
async function submitAction() {
  if (!selected.value || !validAction.value || busy.value || !auth.user) return
  if (!canApprove(auth.user.id, selected.value.supplier_id) && ['approve', 'confirm'].includes(action.value)) return
  const id = selected.value.id, next = action.value, actor = auth.user.id, generation = payoutGeneration, opId = `payout:${id}:${next}`
  const active = () => payoutGeneration === generation && auth.user?.id === actor
  const body = next === 'confirm' ? { external_reference: form.value.external_reference.trim(), evidence: form.value.evidence.trim(), currency: 'USD', amount: form.value.amount, paid_at: new Date(form.value.paid_at).toISOString() } : ['reject', 'fail'].includes(next) ? { evidence: form.value.evidence.trim() } : {}
  let op = operations.get(opId); const wasPending = !!op; busy.value = true; dialogError.value = ''
  try {
    if (!op) { const salt = crypto.randomUUID(); op = { key: crypto.randomUUID(), salt, digest: await fingerprint(body, salt), body }; if (!active()) return; operations.set(opId, op); saveOperation(actor, opId, op) }
    else if (await fingerprint(body, op.salt) !== op.digest) { if (active()) dialogError.value = t('adminMarketplace.retryPayloadMismatch'); return }
    if (!active()) return
    const operation = op; attempted.value = true; statusChecked.value = false
    await stepUp.run(() => active() ? post(`payouts/${id}/${next}`, body, operation.key) : Promise.reject(new Error('Payout action cancelled')))
    forgetOperation(actor, opId, operation)
    if (!active()) return
    closeAction(); closePayout(); await load()
  } catch (e: any) { if (isStepUpCancelled(e) && !wasPending && op) { forgetOperation(actor, opId, op); if (active()) attempted.value = false } else if (active() && !isStepUpCancelled(e)) dialogError.value = e?.message || t('adminMarketplace.actionError') }
  finally { busy.value = false }
}
function openDestination(row: Destination, next: string) { destinationGeneration++; clearSecret(); destination.value = row; destinationAction.value = next; dialogError.value = ''; const op = operations.get(`destination:${row.id}:${next}`); destinationAttempted.value = !!op; destinationInput.value = op?.body ? String((op.body as any)[next === 'reveal' ? 'reason' : 'evidence']) : '' }
function closeDestination() { destinationGeneration++; clearSecret(); destination.value = null; destinationInput.value = ''; dialogError.value = '' }
async function submitDestination() {
  if (!destination.value || !destinationInput.value.trim() || busy.value || !auth.user) return
  const id = destination.value.id, next = destinationAction.value, actor = auth.user.id, generation = destinationGeneration, opId = `destination:${id}:${next}`
  const active = () => generation === destinationGeneration && auth.user?.id === actor
  const body = next === 'reveal' ? { reason: destinationInput.value.trim() } : { evidence: destinationInput.value.trim() }
  let op = operations.get(opId); const wasPending = !!op; busy.value = true; dialogError.value = ''
  try {
    if (!op) { const salt = crypto.randomUUID(); op = { key: crypto.randomUUID(), salt, digest: await fingerprint(body, salt), body }; if (!active()) return; operations.set(opId, op); saveOperation(actor, opId, op) }
    else if (await fingerprint(body, op.salt) !== op.digest) { if (active()) dialogError.value = t('adminMarketplace.retryPayloadMismatch'); return }
    if (!active()) return
    const operation = op; destinationAttempted.value = true
    const result = await stepUp.run(() => active() ? post<{ credential?: string }>(`payout-destinations/${id}/${next}`, body, operation.key) : Promise.reject(new Error('Destination action cancelled')))
    forgetOperation(actor, opId, operation)
    if (!active()) return
    if (next === 'reveal') { secret.value = result.credential || ''; secretTimer = setTimeout(clearSecret, 60_000) } else { closeDestination(); await load() }
  } catch (e: any) { if (isStepUpCancelled(e) && !wasPending && op) { forgetOperation(actor, opId, op); if (active()) destinationAttempted.value = false } else if (active() && !isStepUpCancelled(e)) dialogError.value = e?.message || t('adminMarketplace.actionError') }
  finally { busy.value = false }
}
watch(() => auth.user?.id, (id, previous) => { if (id !== previous) { listGeneration++; payouts.value = []; destinations.value = []; payoutTotal.value = 0; destinationTotal.value = 0; loading.value = false; closeDestination(); closeAction(); closePayout(); operations.clear(); if (id) { restoreOperations(id); void load() } } }, { flush: 'sync' }); onUnmounted(() => { listGeneration++; closeDestination(); closeAction(); closePayout(); operations.clear() }); onMounted(() => { if (auth.user?.id) restoreOperations(auth.user.id); void load() })
</script>
