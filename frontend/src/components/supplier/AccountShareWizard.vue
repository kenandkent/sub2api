<template>
  <BaseDialog :show="true" :z-index="40" :close-on-escape="!locked" :close-on-click-outside="!locked" :show-close-button="!locked" :title="t('marketplace.addAccount')" @close="close">
    <form class="space-y-4" @submit.prevent="submit">
      <p class="text-sm text-gray-500">{{ t('marketplace.wizardStep', { step }) }}</p>
      <div v-if="step === 1" class="space-y-4">
        <p class="text-sm text-gray-500">{{ t('marketplace.eligibility') }}</p>
        <label class="block text-sm">{{ t('marketplace.provider') }}<select v-model="platform" class="input mt-1 w-full"><option v-for="item in options.providers" :key="item.platform" :value="item.platform">{{ item.label }}</option></select></label>
        <label class="block text-sm">{{ t('marketplace.accountType') }}<select v-model="type" class="input mt-1 w-full"><option v-for="kind in provider?.account_types || []" :key="kind" :value="kind">{{ kind }}</option></select></label>
        <label v-if="type === 'api_key'" class="block text-sm">{{ t('marketplace.apiKey') }}<input v-model="apiKey" data-test="api-key" type="password" autocomplete="off" class="input mt-1 w-full" /></label>
        <p v-else class="text-sm text-gray-500">{{ t('marketplace.oauthAfterPolicy') }}</p>
      </div>
      <div v-else-if="step === 2" class="space-y-4">
        <label class="block text-sm">{{ t('marketplace.accountName') }}<input v-model="name" data-test="name" class="input mt-1 w-full" /></label>
        <fieldset><legend class="text-sm">{{ t('marketplace.allowedModels') }}</legend><label v-for="model in provider?.models || []" :key="model" class="mr-4 inline-flex items-center gap-1 text-sm"><input v-model="models" type="checkbox" :value="model" />{{ model }}</label></fieldset>
        <AccountConcurrencyForm v-model="concurrency" :max="options.max_supplier_concurrency" />
      </div>
      <div v-else class="space-y-4">
        <p class="text-sm text-gray-600 dark:text-gray-300">{{ t('marketplace.policySummary', { fee: options.fee_bps, hours: options.hold_hours, version: options.agreement_version }) }}</p>
        <section class="space-y-2"><h3 class="font-semibold">{{ t('marketplace.publishedPrices') }}</h3><p v-if="!pricesAvailable" role="alert" class="text-sm text-amber-600">{{ t('marketplace.priceUnavailable') }}</p><div v-for="price in selectedPrices" :key="`${price.model}-${price.group_name}-${price.price_version}`" class="rounded border border-gray-200 p-3 text-sm dark:border-dark-700"><p>{{ price.model }} · {{ price.public_model }} · {{ price.group_name }} · {{ price.price_version }} · {{ t('marketplace.rateMultiplier') }} {{ price.rate_multiplier }}</p><p v-for="tier in price.tiers" :key="tier.from_tokens">{{ t('marketplace.fromTokens') }} {{ tier.from_tokens }} — {{ t('marketplace.inputPrice') }} {{ tier.input_per_million }} / {{ t('marketplace.outputPrice') }} {{ tier.output_per_million }} / {{ t('marketplace.cacheReadPrice') }} {{ tier.cache_read_per_million }} / {{ t('marketplace.cacheWritePrice') }} {{ tier.cache_write_per_million }}</p></div></section><p v-if="options.agreement_text" class="whitespace-pre-wrap text-sm">{{ options.agreement_text }}</p>
        <a v-if="agreementUrl" :href="agreementUrl" target="_blank" rel="noopener noreferrer" class="text-sm text-primary-600 underline">{{ t('marketplace.readAgreement') }}</a>
        <p v-if="!hasAgreement" class="text-sm text-amber-600">{{ t('marketplace.agreementUnavailable') }}</p>
        <RouterLink to="/available-channels" target="_blank" rel="noopener noreferrer" class="block text-sm text-primary-600 underline">{{ t('marketplace.viewPrices') }}</RouterLink>
        <label class="flex items-start gap-2 text-sm"><input v-model="accepted" type="checkbox" :disabled="locked" />{{ t('marketplace.acceptAgreement') }}</label>
        <div v-if="type === 'oauth' && session && !authorizedId" class="space-y-2 text-sm"><a :href="authUrl" data-test="authorization-link" target="_blank" rel="noopener noreferrer" class="text-primary-600 underline">{{ t('marketplace.openAuthorization') }}</a><p>{{ t('marketplace.callbackHint') }}</p><label class="block">{{ t('marketplace.callbackUrl') }}<input v-model="callback" class="input mt-1 w-full" autocomplete="off" /></label></div>
      </div>
      <p v-if="unknown" role="alert" class="text-sm text-amber-600">{{ t(unknown === 'authorization' ? 'marketplace.authorizationUnknown' : 'marketplace.operationUnknown') }}</p><button v-if="unknown === 'authorization'" data-test="check-authorization" type="button" class="btn btn-secondary" :disabled="busy" @click="checkAuthorization">{{ t('marketplace.checkStatus') }}</button><button v-if="unknown === 'creation' || unknown === 'configuration'" data-test="check-operation" type="button" class="btn btn-secondary" :disabled="busy" @click="checkOperation">{{ t('marketplace.checkStatus') }}</button><button v-if="unknown === 'configuration'" data-test="retry-patch" type="button" class="btn btn-secondary" :disabled="busy" @click="retryPatch">{{ t('marketplace.retrySameSave') }}</button><div v-if="patchConflict" class="space-y-2 text-sm text-amber-600"><p>{{ t('marketplace.versionConflict') }}</p><p>{{ t('marketplace.latestConfiguration') }}: {{ patchConflict.name }} · {{ patchConflict.concurrency }} · {{ patchConflict.offer.allowed_models.join(', ') }}</p><p>{{ t('marketplace.yourConfiguration') }}: {{ name }} · {{ concurrency }} · {{ models.join(', ') }}</p><button type="button" data-test="acknowledge-conflict" class="underline" @click="acknowledgeConflict">{{ t('marketplace.reviewedConfiguration') }}</button></div><p v-if="error" role="alert" class="text-sm text-red-600">{{ error }}</p><button v-if="authorizedId && error" type="button" class="text-sm text-primary-600 underline" @click="openCreated">{{ t('marketplace.openCreatedAccount') }}</button>
      <div class="flex justify-end gap-2">
        <button type="button" data-test="cancel" class="btn btn-secondary" :disabled="locked" @click="close">{{ t('common.cancel') }}</button>
        <button v-if="step > 1" type="button" data-test="back" class="btn btn-secondary" :disabled="locked || !!authorizedId" @click="step--">{{ t('marketplace.previous') }}</button>
        <button v-if="step < 3" type="button" data-test="next" class="btn btn-primary" :disabled="busy || !canAdvance" @click="step++">{{ t('marketplace.next') }}</button>
        <button v-else data-test="create" class="btn btn-primary" :disabled="locked || !!patchConflict || !accepted || !hasAgreement || !pricesAvailable">{{ type === 'oauth' && authorizedId ? t('marketplace.save') : type === 'oauth' && !session ? t('marketplace.startAuthorization') : type === 'oauth' ? t('marketplace.completeAuthorization') : t('marketplace.createDraft') }}</button>
      </div>
    </form>
  </BaseDialog>
</template>
<script setup lang="ts">
import { computed, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { onBeforeRouteLeave, onBeforeRouteUpdate } from 'vue-router'
import BaseDialog from '@/components/common/BaseDialog.vue'
import AccountConcurrencyForm from './AccountConcurrencyForm.vue'
import { createAccount, createAuthorization, completeAuthorization, getAccount, updateAccount, getAuthorizationStatus, getSupplierOperation } from '@/api/supplier'
import { useWorkModeStore } from '@/stores/workMode'
import type { SupplierAccount, SupplierOptions } from '@/types/marketplace'
const props = defineProps<{ options: SupplierOptions }>()
const emit = defineEmits<{ close: []; created: [id?: number] }>()
const { t } = useI18n(); const store = useWorkModeStore()
const step = ref(1); const accepted = ref(false); const platform = ref(props.options.providers[0]?.platform || ''); const type = ref('api_key'); const name = ref(''); const models = ref<string[]>([]); const concurrency = ref(1); const apiKey = ref(''); const busy = ref(false); const error = ref(''); const session = ref(''); const unknown = ref<'' | 'authorization' | 'creation' | 'configuration'>(''); const operationKey = ref(''); const pendingPatch = ref<{ id: number; payload: { expected_version: number; name: string; concurrency: number; allowed_models: string[] } } | null>(null); const patchConflict = ref<SupplierAccount | null>(null); const authUrl = ref(''); const callback = ref(''); const authorizedId = ref<number | null>(null)
const agreementUrl = computed(() => { try { const url = new URL(props.options.agreement_url || ''); return ['http:', 'https:'].includes(url.protocol) ? url.href : '' } catch { return '' } }); const hasAgreement = computed(() => !!props.options.agreement_text?.trim() || !!agreementUrl.value)
const selectedPrices = computed(() => (provider.value?.prices || []).filter(p => models.value.includes(p.model)))
const pricesAvailable = computed(() => models.value.length > 0 && models.value.every(m => selectedPrices.value.some(p => p.model === m)) && selectedPrices.value.every(p => !!p.public_model && !!p.group_name && p.price_version !== undefined && p.price_version !== null && String(p.price_version).length > 0 && typeof p.rate_multiplier === 'string' && !!p.rate_multiplier && !!p.tiers?.length && p.tiers.every(tier => Number.isFinite(tier.from_tokens) && [tier.input_per_million, tier.output_per_million, tier.cache_read_per_million, tier.cache_write_per_million].every(v => typeof v === 'string' && v.trim().length > 0))))
const locked = computed(() => busy.value || !!unknown.value)
onBeforeRouteLeave(() => !locked.value)
onBeforeRouteUpdate(() => !locked.value)
function beforeUnload(event: BeforeUnloadEvent) { if (locked.value) { event.preventDefault(); event.returnValue = '' } }
window.addEventListener('beforeunload', beforeUnload)
const provider = computed(() => props.options.providers.find(p => p.platform === platform.value))
watch(provider, p => { type.value = p?.account_types[0] || ''; models.value = []; session.value = ''; authUrl.value = ''; apiKey.value = '' }, { immediate: true })
watch(type, () => { session.value = ''; authUrl.value = ''; callback.value = ''; apiKey.value = '' })
watch([name, models, concurrency, apiKey, accepted, platform, type], () => { store.dirty = true }, { deep: true })
const configValid = computed(() => !!name.value.trim() && models.value.length > 0 && Number.isInteger(concurrency.value) && concurrency.value > 0 && concurrency.value <= props.options.max_supplier_concurrency)
const canAdvance = computed(() => step.value === 1 ? !!provider.value && !!type.value && (type.value !== 'api_key' || !!apiKey.value) : configValid.value)
function finish(id?: number) { unknown.value = ''; busy.value = false; store.dirty = false; emit('created', id) }
function openCreated() { if (!authorizedId.value) return; callback.value = ''; store.dirty = false; emit('created', authorizedId.value) }
async function close() { if (locked.value || !await store.confirmLeave()) return; apiKey.value = ''; callback.value = ''; store.dirty = false; emit('close') }
onUnmounted(() => { window.removeEventListener('beforeunload', beforeUnload); apiKey.value = ''; callback.value = ''; store.dirty = false })
async function submit() {
  if (step.value !== 3 || !configValid.value || !accepted.value || !hasAgreement.value || !pricesAvailable.value || locked.value || patchConflict.value) return
  busy.value = true; error.value = ''; let inspectConflict = false; let phase: 'create' | 'start' | 'complete' | 'configure' | 'local' = 'local'
  try {
    if (type.value === 'api_key') {
      if (!apiKey.value) { step.value = 1; return }
      operationKey.value ||= crypto.randomUUID(); phase = 'create'; await createAccount({ name: name.value.trim(), platform: platform.value, type: type.value, api_key: apiKey.value, allowed_models: models.value, concurrency: concurrency.value }, operationKey.value)
      apiKey.value = ''; finish(); return
    }
    if (!session.value && !authorizedId.value) {
      phase = 'start'; const result = await createAuthorization({ purpose: 'create', provider: platform.value }); const url = new URL(result.auth_url); if (!['http:', 'https:'].includes(url.protocol)) throw new Error('invalid authorization URL'); authUrl.value = url.href; session.value = result.session_id; return
    }
    if (!authorizedId.value) {
      const url = new URL(callback.value); const state = url.searchParams.get('state'); const code = url.searchParams.get('code'); if (!state || !code) throw new Error('invalid callback')
      phase = 'complete'; authorizedId.value = (await completeAuthorization(session.value, { state, code, purpose: 'create' })).account_id
      callback.value = ''
    }
    phase = 'local'; const account = await getAccount(authorizedId.value)
    pendingPatch.value = { id: account.id, payload: { expected_version: account.offer.version, name: name.value.trim(), concurrency: concurrency.value, allowed_models: [...models.value] } }; phase = 'configure'
    await updateAccount(pendingPatch.value.id, pendingPatch.value.payload)
    finish(account.id)
  } catch (e) {
    apiKey.value = ''; callback.value = ''; const status = (e as { status?: number }).status
    if (phase === 'complete' && ![400, 401, 403, 404, 410].includes(status || 0)) unknown.value = 'authorization'
    else if (phase === 'create' && ![400, 401, 403, 404, 410, 422].includes(status || 0)) unknown.value = 'creation'
    else if (phase === 'configure' && (status === 409 || ![400, 401, 403, 404, 410, 422].includes(status || 0))) { unknown.value = 'configuration'; inspectConflict = status === 409 }
    else { if (phase === 'complete') { session.value = ''; authUrl.value = '' } if (phase === 'create') operationKey.value = ''; error.value = status === 409 ? t('marketplace.versionConflict') : t('marketplace.authorizationFailed') }
  } finally { busy.value = false }
  if (unknown.value === 'authorization') await checkAuthorization()
  else if (unknown.value === 'configuration' && inspectConflict) await checkOperation()
}
async function checkAuthorization() { if (!session.value || busy.value) return; busy.value = true; try { const result = await getAuthorizationStatus(session.value); if (result.status === 'completed' && result.account_id) { authorizedId.value = result.account_id; unknown.value = '' } else if (result.status === 'failed') { session.value = ''; authUrl.value = ''; unknown.value = ''; error.value = t('marketplace.authorizationFailed') } } catch { /* Keep the uncertain state until the server confirms a result. */ } finally { busy.value = false } }
async function checkOperation() { if (busy.value) return; busy.value = true; try { if (unknown.value === 'creation') { const result = await getSupplierOperation(operationKey.value); if (result.status === 'completed' && result.account_id) { finish(result.account_id) } else if (result.status === 'failed') { operationKey.value = ''; unknown.value = ''; error.value = t('marketplace.authorizationFailed') } } else if (unknown.value === 'configuration' && pendingPatch.value) { const account = await getAccount(pendingPatch.value.id); const snapshot = pendingPatch.value.payload; if (account.offer.version > snapshot.expected_version) { if (account.name === snapshot.name && account.concurrency === snapshot.concurrency && [...account.offer.allowed_models].sort().join('\0') === [...snapshot.allowed_models].sort().join('\0')) finish(account.id); else { unknown.value = ''; patchConflict.value = account; store.dirty = true } } } } catch { /* Keep the uncertain state. */ } finally { busy.value = false } }
async function retryPatch() { if (busy.value || unknown.value !== 'configuration' || !pendingPatch.value) return; busy.value = true; let inspect = false; try { await updateAccount(pendingPatch.value.id, pendingPatch.value.payload); finish(pendingPatch.value.id) } catch (e) { if ((e as { status?: number }).status === 409) inspect = true } finally { busy.value = false } if (inspect) await checkOperation() }
function acknowledgeConflict() { patchConflict.value = null; pendingPatch.value = null; error.value = '' }
</script>
