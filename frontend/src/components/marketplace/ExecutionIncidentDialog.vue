<template>
  <BaseDialog :show="show" :title="t('adminMarketplace.executionIncident')" @close="close">
    <p class="mb-4 rounded-lg bg-amber-50 p-3 text-sm text-amber-900 dark:bg-amber-900/20 dark:text-amber-200">{{ t('adminMarketplace.incidentRecordOnly') }}</p>
    <div class="space-y-4">
      <label class="block text-sm">{{ t('adminMarketplace.incidentReceipt') }}<input v-model="form.receipt" data-test="incident-receipt" class="input mt-1" :disabled="locked" autocomplete="off" /></label>
      <label class="block text-sm">{{ t('adminMarketplace.incidentAttempt') }}<input v-model="form.attempt" data-test="incident-attempt" class="input mt-1" :disabled="locked" inputmode="numeric" /></label>
      <label class="block text-sm">{{ t('adminMarketplace.incidentRelease') }}<input v-model="form.release" data-test="incident-release" class="input mt-1" :disabled="locked" maxlength="255" autocomplete="off" /></label>
      <label class="block text-sm">{{ t('adminMarketplace.incidentEvidence') }}<textarea v-model="form.evidence" class="input mt-1" :disabled="locked" rows="4" maxlength="4000" /></label>
      <p class="text-sm text-gray-500">{{ t('adminMarketplace.incidentAssociation') }}</p>
      <p v-if="unknown" class="text-sm text-amber-700 dark:text-amber-300">{{ t('adminMarketplace.incidentUnknown') }}</p>
      <p v-if="error" role="alert" class="text-sm text-red-600">{{ error }}</p>
      <p v-if="result" role="status" class="text-sm text-primary-700 dark:text-primary-300">{{ t('adminMarketplace.incidentRecorded') }}</p>
      <section v-if="records !== null" class="space-y-2 text-sm">
        <h3 class="font-semibold">{{ t('adminMarketplace.incidentRecords') }}</h3>
        <p v-if="!records.length" class="text-gray-500">{{ t('adminMarketplace.incidentEmpty') }}</p>
        <article v-for="record in records" :key="record.operation_id" class="space-y-1 break-words rounded-lg border border-gray-200 p-3 dark:border-dark-600">
          <p>{{ t('adminMarketplace.incidentAttempt') }}: {{ record.attempt_id }} · {{ record.created_at }}</p>
          <p class="break-all">{{ t('adminMarketplace.incidentRelease') }}: {{ record.release_operation_id }}</p>
          <p class="whitespace-pre-wrap">{{ record.evidence }}</p>
        </article>
      </section>
    </div>
    <template #footer>
      <div class="flex w-full flex-wrap justify-end gap-2">
        <button class="btn btn-secondary" :disabled="busy" @click="close">{{ t('common.close') }}</button>
        <button class="btn btn-secondary" :disabled="busy || unknown" @click="reset">{{ t('adminMarketplace.incidentNew') }}</button>
        <button data-test="incident-check" class="btn btn-secondary" :disabled="busy || !validReceipt" @click="check">{{ t('adminMarketplace.incidentRecords') }}</button>
        <button data-test="incident-submit" class="btn btn-primary" :disabled="busy || !!result || (!operation && !valid)" @click="submit">{{ operation ? t('adminMarketplace.retryAction') : t('adminMarketplace.incidentRegister') }}</button>
      </div>
    </template>
  </BaseDialog>
  <TotpStepUpDialog :controller="stepUp" />
</template>
<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import TotpStepUpDialog from '@/components/auth/TotpStepUpDialog.vue'
import { useStepUp, isStepUpCancelled } from '@/composables/useStepUp'
import { get, post, type ExecutionIncident } from '@/api/admin/marketplace'

defineProps<{ show: boolean }>()
const emit = defineEmits<{ close: [] }>()
const { t } = useI18n()
const stepUp = useStepUp()
const form = ref({ receipt: '', attempt: '', release: '', evidence: '' })
const busy = ref(false), unknown = ref(false), error = ref('')
const result = ref<ExecutionIncident | null>(null), records = ref<ExecutionIncident[] | null>(null)
const operation = ref<{ key: string; path: string; body: { attempt_id: number; release_operation_id: string; evidence: string } } | null>(null)
const validReceipt = computed(() => /^[0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12}$/i.test(form.value.receipt.trim()))
const bytes = (value: string) => new TextEncoder().encode(value.trim()).length
const valid = computed(() => validReceipt.value && /^[1-9]\d*$/.test(form.value.attempt) && Number.isSafeInteger(Number(form.value.attempt)) && bytes(form.value.release) > 0 && bytes(form.value.release) <= 255 && bytes(form.value.evidence) > 0 && bytes(form.value.evidence) <= 4000)
const locked = computed(() => busy.value || !!operation.value || !!result.value)
function close() { if (!busy.value) emit('close') }
function reset() { if (busy.value || unknown.value) return; form.value = { receipt: '', attempt: '', release: '', evidence: '' }; operation.value = null; result.value = null; records.value = null; error.value = '' }
async function submit() {
  if (busy.value || result.value || (!operation.value && !valid.value)) return
  if (!operation.value) operation.value = { key: crypto.randomUUID(), path: `receipts/${form.value.receipt.trim()}/execution-incidents`, body: { attempt_id: Number(form.value.attempt), release_operation_id: form.value.release.trim(), evidence: form.value.evidence.trim() } }
  const op = operation.value
  busy.value = true; error.value = ''
  try { result.value = await stepUp.run(() => post<ExecutionIncident>(op.path, op.body, op.key)); unknown.value = false }
  catch (e) { if (!isStepUpCancelled(e)) { const status = (e as { status?: number }).status; unknown.value = !status || status >= 500; error.value = t('adminMarketplace.actionError') } }
  finally { busy.value = false }
}
async function check() {
  if (busy.value || !validReceipt.value) return
  busy.value = true; error.value = ''
  try { const response = await stepUp.run(() => get<{ items: ExecutionIncident[] }>(`receipts/${form.value.receipt.trim()}/execution-incidents`)); records.value = response.items; const found = response.items.find(item => item.operation_id === operation.value?.key); if (found) { result.value = found; unknown.value = false } }
  catch (e) { if (!isStepUpCancelled(e)) error.value = t('adminMarketplace.loadError') }
  finally { busy.value = false }
}
</script>
