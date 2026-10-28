<template>
  <BaseDialog :show="show" :title="t('adminFundingTools.title')" width="wide" @close="close">
    <div class="space-y-5">
      <p class="text-sm text-gray-600 dark:text-dark-300">{{ t('adminFundingTools.intro') }}</p>
      <div class="flex flex-wrap gap-2 border-b border-gray-200 pb-3 dark:border-dark-700" role="tablist">
        <button v-for="item in tabs" :key="item" :data-test="`tab-${item}`" type="button" role="tab" :aria-selected="tab === item" :disabled="busy || storageBlocked" :class="tab === item ? 'btn btn-primary btn-sm' : 'btn btn-secondary btn-sm'" @click="selectTab(item)">{{ t(`adminFundingTools.tab_${item}`) }}</button>
      </div>
      <p v-if="storageBlocked" role="alert" class="rounded-lg bg-red-50 p-3 text-sm text-red-700 dark:bg-red-900/20 dark:text-red-200">{{ t('adminFundingTools.storageBlocked') }}</p>
      <section v-if="operations.length" class="rounded-lg border border-amber-200 bg-amber-50 p-3 text-sm dark:border-amber-900 dark:bg-amber-900/20" aria-labelledby="pending-operations-title">
        <h4 id="pending-operations-title" class="font-medium">{{ t('adminFundingTools.pendingTitle') }}</h4>
        <ul class="mt-2 space-y-2"><li v-for="operation in operations" :key="operation.key" class="flex flex-wrap items-center justify-between gap-2"><span class="min-w-0 break-all">{{ t(`adminFundingTools.tab_${operation.kind}`) }} · {{ operation.target }} · {{ operation.key }}</span><button data-test="pending-resume" class="btn btn-secondary btn-sm" :disabled="busy" @click="resume(operation)">{{ t('adminFundingTools.resume') }}</button></li></ul>
      </section>
      <p v-if="pending" role="status" class="rounded-lg bg-amber-50 p-3 text-sm text-amber-900 dark:bg-amber-900/20 dark:text-amber-200">{{ t('adminFundingTools.unknown') }} <span class="break-all">{{ pending.key }}</span> {{ t('adminFundingTools.reenter') }}</p>
      <p v-if="error" role="alert" class="text-sm text-red-700 dark:text-red-300">{{ error }}</p>

      <section v-if="tab === 'reclass'" class="space-y-4" aria-labelledby="reclass-title">
        <div><h4 id="reclass-title" class="font-semibold">{{ t('adminFundingTools.reclassTitle') }}</h4><p class="mt-1 text-sm text-gray-600 dark:text-dark-300">{{ t('adminFundingTools.reclassWarning') }}</p></div>
        <div class="grid gap-4 sm:grid-cols-2">
          <label class="block text-sm">{{ t('adminFundingTools.userId') }}<input v-model.trim="reclass.user" data-test="reclass-user" class="input mt-1 w-full" inputmode="numeric" autocomplete="off" :disabled="busy || storageBlocked" /></label>
          <label class="block text-sm">{{ t('adminFundingTools.orderId') }}<input v-model.trim="reclass.order" data-test="reclass-order" class="input mt-1 w-full" inputmode="numeric" autocomplete="off" :disabled="busy || storageBlocked" /></label>
        </div>
        <label class="block text-sm">{{ t('adminFundingTools.amount') }}<input v-model.trim="reclass.amount" data-test="reclass-amount" class="input mt-1 w-full" inputmode="decimal" autocomplete="off" :disabled="busy || storageBlocked" /></label>
        <label class="block text-sm">{{ t('adminFundingTools.evidence') }}<textarea v-model="reclass.evidence" data-test="reclass-evidence" class="input mt-1 w-full" rows="3" :disabled="busy || storageBlocked" /></label>
        <label class="flex items-start gap-3 rounded-lg border border-amber-200 bg-amber-50 p-3 text-sm text-amber-900 dark:border-amber-900 dark:bg-amber-900/20 dark:text-amber-200"><input v-model="reclass.confirmed" data-test="reclass-confirm" type="checkbox" class="mt-1" :disabled="busy || storageBlocked" /><span>{{ t('adminFundingTools.provenanceConfirm') }}</span></label>
        <dl v-if="reclassResult" data-test="reclass-result" class="grid grid-cols-2 gap-2 rounded-lg bg-gray-50 p-4 text-sm dark:bg-dark-800"><dt>{{ t('adminFundingTools.reclassAmount') }}</dt><dd>{{ reclassResult.amount }}</dd><dt>{{ t('adminFundingTools.cashAmount') }}</dt><dd>{{ reclassResult.cash_amount }}</dd><dt>{{ t('adminFundingTools.bonusAmount') }}</dt><dd>{{ reclassResult.bonus_amount }}</dd></dl>
        <button v-if="!pending" data-test="reclass-submit" class="btn btn-primary" :disabled="busy || storageBlocked || !validReclass" @click="submit('reclass')">{{ t('adminFundingTools.reclassAction') }}</button>
        <button v-else data-test="reclass-retry" class="btn btn-primary" :disabled="busy || storageBlocked || !validReclass" @click="submit('reclass')">{{ t('adminFundingTools.retry') }}</button>
      </section>

      <section v-else-if="tab === 'risk'" class="space-y-4" aria-labelledby="risk-title">
        <div class="flex flex-wrap items-start justify-between gap-3"><div><h4 id="risk-title" class="font-semibold">{{ t('adminFundingTools.riskTitle') }}</h4><p class="mt-1 text-sm text-gray-600 dark:text-dark-300">{{ t('adminFundingTools.riskHint') }}</p></div><button class="btn btn-secondary btn-sm" :disabled="busy || loadingRisks" @click="loadRisks">{{ t('adminFundingTools.refresh') }}</button></div>
        <p v-if="loadingRisks" class="text-sm text-gray-500">{{ t('adminFundingTools.loading') }}</p>
        <p v-else-if="riskLoaded && !risks.length" class="text-sm text-gray-500">{{ t('adminFundingTools.noRisks') }}</p>
        <ul v-else class="divide-y divide-gray-200 rounded-lg border border-gray-200 dark:divide-dark-700 dark:border-dark-700">
          <li v-for="row in risks" :key="row.user_id" class="flex flex-wrap items-center justify-between gap-3 p-3 text-sm">
            <div class="min-w-0"><strong>{{ t('adminFundingTools.userId') }} {{ row.user_id }}</strong><p class="mt-1 break-words text-gray-600 dark:text-dark-300">{{ row.reason }}</p><p class="mt-1 text-gray-500">{{ t('adminFundingTools.debt') }} {{ row.debt }} · {{ t('adminFundingTools.version') }} {{ row.version }}</p><p v-if="!zeroDebt(row.debt)" class="mt-1 text-amber-700 dark:text-amber-300">{{ t('adminFundingTools.debtBlocked') }}</p></div>
            <button :data-test="`risk-clear-${row.user_id}`" class="btn btn-secondary btn-sm" :disabled="busy || !!pending || !zeroDebt(row.debt)" @click="chooseRisk(row)">{{ t('adminFundingTools.reviewRisk') }}</button>
          </li>
        </ul>
        <div v-if="riskSelected || pending?.kind === 'risk'" class="space-y-3 border-t border-gray-200 pt-4 dark:border-dark-700">
          <p class="text-sm font-medium">{{ t('adminFundingTools.riskReview') }}</p>
          <div class="grid gap-3 sm:grid-cols-2"><label class="block text-sm">{{ t('adminFundingTools.userId') }}<input v-model.trim="risk.user" data-test="risk-user" class="input mt-1 w-full" inputmode="numeric" :disabled="busy || !pending" /></label><label class="block text-sm">{{ t('adminFundingTools.version') }}<input v-model.trim="risk.version" data-test="risk-version" class="input mt-1 w-full" inputmode="numeric" :disabled="busy || !pending" /></label></div>
          <label class="block text-sm">{{ t('adminFundingTools.evidence') }}<textarea v-model="risk.evidence" data-test="risk-evidence" class="input mt-1 w-full" rows="3" :disabled="busy || storageBlocked" /></label>
          <button v-if="!pending" data-test="risk-submit" class="btn btn-primary" :disabled="busy || storageBlocked || !validRisk" @click="submit('risk')">{{ t('adminFundingTools.clearRisk') }}</button>
          <button v-else data-test="risk-retry" class="btn btn-primary" :disabled="busy || storageBlocked || !validRisk" @click="submit('risk')">{{ t('adminFundingTools.retry') }}</button>
        </div>
      </section>

      <section v-else class="space-y-4" aria-labelledby="vault-title">
        <div><h4 id="vault-title" class="font-semibold">{{ t('adminFundingTools.vaultTitle') }}</h4><p class="mt-1 text-sm text-gray-600 dark:text-dark-300">{{ t('adminFundingTools.vaultHint') }}</p></div>
        <label class="block text-sm">{{ t('adminFundingTools.batchLimit') }}<input v-model.trim="vault.limit" data-test="vault-limit" class="input mt-1 w-full" inputmode="numeric" :disabled="busy || storageBlocked" /></label>
        <label class="block text-sm">{{ t('adminFundingTools.reason') }}<textarea v-model="vault.reason" data-test="vault-reason" class="input mt-1 w-full" rows="3" :disabled="busy || storageBlocked" /></label>
        <dl v-if="vaultResult" data-test="vault-result" class="grid grid-cols-2 gap-2 rounded-lg bg-gray-50 p-4 text-sm dark:bg-dark-800"><dt>{{ t('adminFundingTools.processed') }}</dt><dd>{{ vaultResult.processed }}</dd><dt>{{ t('adminFundingTools.remaining') }}</dt><dd>{{ vaultResult.remaining }}</dd><dt>{{ t('adminFundingTools.completed') }}</dt><dd>{{ vaultResult.completed ? t('adminFundingTools.yes') : t('adminFundingTools.no') }}</dd><dt>{{ t('adminFundingTools.targetKey') }}</dt><dd class="break-all">{{ vaultResult.target_key_id }}</dd></dl>
        <button v-if="pending" data-test="vault-retry" class="btn btn-primary" :disabled="busy || storageBlocked || !validVault" @click="submit('vault')">{{ t('adminFundingTools.retry') }}</button>
        <button v-else-if="vaultResult && !vaultResult.completed" data-test="vault-next" class="btn btn-primary" :disabled="busy || storageBlocked || !validVault" @click="submit('vault')">{{ t('adminFundingTools.nextBatch') }}</button>
        <button v-else data-test="vault-submit" class="btn btn-primary" :disabled="busy || storageBlocked || !validVault" @click="submit('vault')">{{ t('adminFundingTools.startBatch') }}</button>
      </section>
      <p v-if="success" data-test="success" role="status" class="rounded-lg bg-green-50 p-3 text-sm text-green-800 dark:bg-green-900/20 dark:text-green-200">{{ success }}</p>
    </div>
    <template #footer><button class="btn btn-secondary" :disabled="busy" @click="close">{{ t('adminFundingTools.close') }}</button></template>
  </BaseDialog>
  <TotpStepUpDialog :controller="stepUp" />
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import TotpStepUpDialog from '@/components/auth/TotpStepUpDialog.vue'
import { useStepUp, isStepUpCancelled } from '@/composables/useStepUp'
import { useAuthStore } from '@/stores/auth'
import { get, post } from '@/api/admin/marketplace'

const props = defineProps<{ show: boolean }>()
const emit = defineEmits<{ (event: 'close'): void }>()
type Tool = 'reclass' | 'risk' | 'vault'
type Pending = { kind: Tool; key: string; target: string; phase: 'uncertain' | 'succeeded' | 'rejected' | 'cancelled'; salt: string; hash: string }
type RiskRow = { user_id: number; version: number; reason: string; debt: string }
type ReclassResult = { amount: string; cash_amount: string; bonus_amount: string }
type VaultResult = { operation_id: string; processed: number; remaining: number; completed: boolean; target_key_id: string }
const tabs: Tool[] = ['reclass', 'risk', 'vault']
const { t } = useI18n(), auth = useAuthStore(), stepUp = useStepUp()
const tab = ref<Tool>('reclass'), pending = ref<Pending | null>(null), operations = ref<Pending[]>([]), busy = ref(false), loadingRisks = ref(false), riskLoaded = ref(false), storageBlocked = ref(false)
const error = ref(''), success = ref(''), risks = ref<RiskRow[]>([]), riskSelected = ref(false), vaultResult = ref<VaultResult | null>(null), reclassResult = ref<ReclassResult | null>(null)
const reclass = reactive({ user: '', order: '', amount: '', evidence: '', confirmed: false })
const risk = reactive({ user: '', version: '', evidence: '' })
const vault = reactive({ limit: '50', reason: '' })
let generation = 0
let riskGeneration = 0
const integer = (value: string) => /^[1-9]\d{0,14}$/.test(value) && Number.isSafeInteger(Number(value))
const validMoney = (value: string) => /^(?:0|[1-9]\d{0,11})(?:\.\d{1,8})?$/.test(value) && !/^0(?:\.0+)?$/.test(value)
const validText = (value: string, limit = 4000) => !!value.trim() && new TextEncoder().encode(value.trim()).length <= limit
const zeroDebt = (value: string) => /^0+(?:\.0+)?$/.test(String(value))
const validReclass = computed(() => integer(reclass.user) && integer(reclass.order) && validMoney(reclass.amount) && validText(reclass.evidence) && reclass.confirmed)
const validRisk = computed(() => integer(risk.user) && integer(risk.version) && validText(risk.evidence) && (!!pending.value || !!risks.value.find(row => row.user_id === Number(risk.user) && row.version === Number(risk.version) && zeroDebt(row.debt))))
const validVault = computed(() => /^\d{1,3}$/.test(vault.limit) && Number(vault.limit) >= 1 && Number(vault.limit) <= 200 && validText(vault.reason, 4096))
const storagePrefix = (actor: number) => `funding-admin-operation:v2:${actor}:`
const storageKey = (actor: number, key: string) => storagePrefix(actor) + key
const legacyStorageKey = (actor: number) => `funding-admin-operation:${actor}`
const changeEvent = 'funding-admin-operations-changed'
function clearFields() { Object.assign(reclass, { user: '', order: '', amount: '', evidence: '', confirmed: false }); Object.assign(risk, { user: '', version: '', evidence: '' }); Object.assign(vault, { limit: '50', reason: '' }); risks.value = []; riskLoaded.value = false; riskSelected.value = false; vaultResult.value = null; reclassResult.value = null; error.value = ''; success.value = ''; busy.value = false; loadingRisks.value = false }
function parsePending(raw: string): Pending {
  const value = JSON.parse(raw) as Pending
  if (!tabs.includes(value.kind) || typeof value.key !== 'string' || !/^[0-9a-f-]{36}$/i.test(value.key) || typeof value.target !== 'string' || !value.target || value.target.length > 32 || !['uncertain', 'succeeded', 'rejected', 'cancelled'].includes(value.phase) || typeof value.salt !== 'string' || !/^[0-9a-f-]{36}$/i.test(value.salt) || !/^[a-f0-9]{64}$/.test(value.hash)) throw new Error('invalid operation metadata')
  return value
}
function readRecords(actor: number): Pending[] {
  const out: Pending[] = [], prefix = storagePrefix(actor)
  for (let i = 0; i < localStorage.length; i++) {
    const key = localStorage.key(i); if (!key?.startsWith(prefix)) continue
    const raw = localStorage.getItem(key); if (!raw) throw new Error('missing operation metadata')
    const value = parsePending(raw); if (key !== storageKey(actor, value.key)) throw new Error('operation metadata mismatch')
    if (value.phase === 'uncertain') out.push(value)
  }
  return out
}
function sameIdentity(a: Pending, b: Pending) {
  return a.kind === b.kind && a.key === b.key && a.target === b.target && a.salt === b.salt && a.hash === b.hash
}
class OperationResolvedElsewhere extends Error {}
// Migrate the previous session-only metadata without losing an unresolved key.
function migrateLegacy(actor: number) {
  const raw = sessionStorage.getItem(legacyStorageKey(actor)); if (!raw) return
  const value = parsePending(raw), key = storageKey(actor, value.key), existing = localStorage.getItem(key)
  if (existing && !sameIdentity(parsePending(existing), value)) throw new Error('operation metadata conflict')
  if (!existing) localStorage.setItem(key, raw)
  sessionStorage.removeItem(legacyStorageKey(actor))
}
function refreshRecords(actor: number) {
  try { migrateLegacy(actor); operations.value = readRecords(actor); storageBlocked.value = false }
  catch { operations.value = []; storageBlocked.value = true }
}
function notifyRecords(actor: number) { window.dispatchEvent(new CustomEvent(changeEvent, { detail: actor })) }
function onRecordChange(event: Event) {
  const actor = auth.user?.id; if (!actor || !props.show) return
  if (event instanceof StorageEvent && event.key && !event.key.startsWith(storagePrefix(actor))) return
  if (event instanceof CustomEvent && event.detail !== actor) return
  refreshRecords(actor)
}
function resume(value: Pending) {
  if (busy.value) return
  generation++; riskGeneration++; clearFields(); pending.value = value; tab.value = value.kind
  if (value.kind === 'risk') risk.user = value.target
  if (value.kind === 'reclass') reclass.order = value.target
}
function reset() {
  generation++; riskGeneration++; clearFields(); tab.value = 'reclass'; pending.value = null; operations.value = []; storageBlocked.value = false
  if (props.show && auth.user?.id) { refreshRecords(auth.user.id); if (operations.value.length) resume(operations.value[0]) }
}
watch(() => [props.show, auth.user?.id] as const, reset, { immediate: true, flush: 'sync' })
onMounted(() => { window.addEventListener('storage', onRecordChange); window.addEventListener(changeEvent, onRecordChange) })
onUnmounted(() => { generation++; riskGeneration++; clearFields(); window.removeEventListener('storage', onRecordChange); window.removeEventListener(changeEvent, onRecordChange) })
function close() { generation++; riskGeneration++; clearFields(); emit('close') }
function active(actor: number, token: number) { return props.show && auth.user?.id === actor && generation === token }
async function withLock<T>(name: string, work: () => Promise<T>): Promise<T> {
  if (!navigator.locks || typeof navigator.locks.request !== 'function') throw new Error('operation coordination unavailable')
  return navigator.locks.request(name, { mode: 'exclusive' }, work)
}
async function savePending(actor: number, operation: Pending, previous: Pending | null): Promise<Pending> {
  return withLock(`funding-admin-records:${actor}`, async () => {
    migrateLegacy(actor)
    const records = readRecords(actor), sameTarget = records.find(item => item.kind === operation.kind && item.target === operation.target && item.key !== operation.key)
    if (sameTarget && !previous) return sameTarget
    const key = storageKey(actor, operation.key), existing = localStorage.getItem(key)
    if (existing) {
      const saved = parsePending(existing)
      if (!sameIdentity(saved, operation)) throw new Error('operation metadata conflict')
      return saved
    }
    // A missing previously recorded operation is not proof of its outcome.
    // Never silently resurrect it after another tab has resolved it.
    if (previous) throw new Error('operation recovery record missing')
    localStorage.setItem(key, JSON.stringify(operation))
    notifyRecords(actor); return operation
  })
}
async function markTerminal(actor: number, operation: Pending, phase: 'succeeded' | 'rejected' | 'cancelled') {
  await withLock(`funding-admin-records:${actor}`, async () => {
    const key = storageKey(actor, operation.key), raw = localStorage.getItem(key)
    if (raw && !sameIdentity(parsePending(raw), operation)) throw new Error('operation metadata conflict')
    // Retain non-secret terminal metadata so queued retries never dispatch an
    // operation after a different tab has confirmed or rejected it.
    localStorage.setItem(key, JSON.stringify({ ...operation, phase }))
    notifyRecords(actor)
  })
}

async function loadRisks() {
  const actor = auth.user?.id; if (!actor || !props.show) return
  const token = generation, request = ++riskGeneration; loadingRisks.value = true; error.value = ''
  try { const rows = await stepUp.run(() => active(actor, token) && request === riskGeneration ? get<RiskRow[]>('funding-risks') : Promise.reject(new Error('risk review no longer active'))); if (active(actor, token) && request === riskGeneration) { risks.value = Array.isArray(rows) ? rows : []; riskLoaded.value = true } }
  catch (e) { if (active(actor, token) && request === riskGeneration && !isStepUpCancelled(e)) error.value = t('adminFundingTools.loadError') }
  finally { if (active(actor, token) && request === riskGeneration) loadingRisks.value = false }
}
function selectTab(next: Tool) { pending.value = null; tab.value = next; error.value = ''; success.value = ''; if (next === 'risk') void loadRisks() }
function chooseRisk(row: RiskRow) { if (!zeroDebt(row.debt) || pending.value) return; Object.assign(risk, { user: String(row.user_id), version: String(row.version), evidence: '' }); riskSelected.value = true; error.value = '' }
async function digest(salt: string, kind: Tool, target: string, path: string, body: unknown): Promise<string> {
  const bytes = new TextEncoder().encode(JSON.stringify([salt, kind, target, path, body]))
  const result = await crypto.subtle.digest('SHA-256', bytes)
  return Array.from(new Uint8Array(result), byte => byte.toString(16).padStart(2, '0')).join('')
}
function payload(kind: Tool, key: string): { target: string; path: string; body: Record<string, unknown> } {
  if (kind === 'reclass') return { target: reclass.order, path: 'funding-reclassifications', body: { user_id: Number(reclass.user), order_id: Number(reclass.order), amount: reclass.amount, evidence: reclass.evidence.trim(), remaining_provenance_confirmed: reclass.confirmed } }
  if (kind === 'risk') return { target: risk.user, path: `funding-risks/${risk.user}/clear`, body: { expected_version: Number(risk.version), evidence: risk.evidence.trim() } }
  return { target: 'vault', path: 'vault/rotate', body: { operation_id: key, limit: Number(vault.limit), reason: vault.reason.trim() } }
}
function failureReason(e: unknown): string {
  return (e && typeof e === 'object' && 'reason' in e) ? String((e as { reason?: unknown }).reason || '') : ''
}
function knownNoEffect(e: unknown, wasPending: boolean): boolean {
  const reason = failureReason(e)
  return (isStepUpCancelled(e) && !wasPending) || reason === 'FUNDING_OPERATION_REJECTED' || reason === 'FUNDING_RISK_STALE' || (!wasPending && (reason === 'FUNDING_INPUT_INVALID' || reason === 'VAULT_INPUT_INVALID'))
}
async function submit(kind: Tool) {
  const actor = auth.user?.id
  if (!actor || busy.value || storageBlocked.value || (pending.value && pending.value.kind !== kind) || (kind === 'reclass' && !validReclass.value) || (kind === 'risk' && !validRisk.value) || (kind === 'vault' && !validVault.value)) return
  const token = generation, previous = pending.value, wasPending = !!previous
  busy.value = true; error.value = ''; success.value = ''
  let operation: Pending | null = null
  try {
    const key = previous?.key || crypto.randomUUID(), salt = previous?.salt || crypto.randomUUID()
    const action = payload(kind, key), bodyBytes = new TextEncoder().encode(JSON.stringify(action.body)).length
    if (bodyBytes > (kind === 'vault' ? 8192 : 32768)) { error.value = t('adminFundingTools.bodyTooLarge'); return }
    const hash = await digest(salt, kind, action.target, action.path, action.body)
    if (!active(actor, token)) return
    if (previous && (previous.target !== action.target || previous.hash !== hash)) { error.value = t('adminFundingTools.bodyMismatch'); return }
    operation = previous || { kind, key, target: action.target, phase: 'uncertain', salt, hash }
    try {
      const saved = await savePending(actor, operation, previous)
      if (!active(actor, token)) return
      if (saved.key !== key) { pending.value = saved; error.value = t('adminFundingTools.unknown'); return }
      if (saved.phase !== 'uncertain') { pending.value = null; error.value = t('adminFundingTools.resolvedElsewhere'); return }
      pending.value = saved
    } catch { if (active(actor, token)) { storageBlocked.value = true; error.value = t('adminFundingTools.storageBlocked') } return }
    const confirmedOperation = operation
    // Only one browser tab may submit this operation at a time; the backend
    // idempotency key remains the ultimate guard against a lost response.
    const result = await withLock(`funding-admin-operation:${actor}:${key}`, async () => {
      try {
        // Recheck under the operation lock: a different tab may already have
        // recorded a terminal result while this retry was waiting.
        const current = await savePending(actor, confirmedOperation, previous)
        if (current.key !== key) throw new Error('another operation is unresolved')
        if (current.phase !== 'uncertain') throw new OperationResolvedElsewhere()
        const value = await stepUp.run(() => active(actor, token) ? post<Record<string, unknown>>(action.path, action.body, key) : Promise.reject(new Error('action no longer active')))
        if (active(actor, token)) await markTerminal(actor, confirmedOperation, 'succeeded')
        return value
      } catch (e) {
        // Keep the per-operation lock until the recovery record is removed;
        // no other tab may submit the same key between rejection and cleanup.
        if (active(actor, token) && knownNoEffect(e, wasPending)) await markTerminal(actor, confirmedOperation, isStepUpCancelled(e) ? 'cancelled' : 'rejected')
        throw e
      }
    })
    if (!active(actor, token)) return
    pending.value = null
    if (kind === 'vault') vaultResult.value = result as unknown as VaultResult
    else { success.value = t(kind === 'risk' ? 'adminFundingTools.riskCleared' : 'adminFundingTools.reclassDone'); if (kind === 'reclass') { reclassResult.value = result as unknown as ReclassResult; reclass.evidence = '' } else { risk.evidence = ''; riskSelected.value = false; void loadRisks() } }
  } catch (e) {
    if (!active(actor, token)) return
    const reason = failureReason(e), noEffect = knownNoEffect(e, wasPending)
    if (e instanceof OperationResolvedElsewhere) {
      pending.value = null; error.value = t('adminFundingTools.resolvedElsewhere')
    } else if (noEffect) {
      pending.value = null
      if (reason === 'FUNDING_RISK_STALE') { risks.value = []; riskLoaded.value = false; riskSelected.value = false; Object.assign(risk, { user: '', version: '', evidence: '' }) }
      if (!isStepUpCancelled(e)) error.value = t(reason === 'FUNDING_RISK_STALE' ? 'adminFundingTools.staleRisk' : 'adminFundingTools.rejected')
    } else if (!isStepUpCancelled(e)) error.value = t(reason === 'FUNDING_OPERATION_CONFLICT' || reason === 'VAULT_OPERATION_CONFLICT' ? 'adminFundingTools.operationConflict' : 'adminFundingTools.unknownError')
  } finally { if (active(actor, token)) busy.value = false }
}
</script>
