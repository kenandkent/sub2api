<template>
  <BaseDialog :show="true" :z-index="40" :title="t('marketplace.addAccount')" @close="close">
    <form class="space-y-4" @submit.prevent="create">
      <p class="text-sm text-gray-500">{{ t('marketplace.eligibility') }}</p>
      <label class="block text-sm">{{ t('marketplace.provider') }}<select v-model="platform" class="input mt-1 w-full"><option v-for="provider in options.providers" :key="provider.platform" :value="provider.platform">{{ provider.label }}</option></select></label>
      <label class="block text-sm">{{ t('marketplace.accountType') }}<select v-model="type" class="input mt-1 w-full"><option v-for="kind in provider?.account_types || []" :key="kind" :value="kind">{{ kind }}</option></select></label>
      <label v-if="type === 'api_key'" class="block text-sm">{{ t('marketplace.accountName') }}<input v-model="name" data-test="name" class="input mt-1 w-full" required /></label>
      <fieldset v-if="type === 'api_key'"><legend class="text-sm">{{ t('marketplace.allowedModels') }}</legend><label v-for="model in provider?.models || []" :key="model" class="mr-4 inline-flex items-center gap-1 text-sm"><input v-model="models" type="checkbox" :value="model" />{{ model }}</label></fieldset>
      <AccountConcurrencyForm v-if="type === 'api_key'" v-model="concurrency" :max="options.max_supplier_concurrency" />
      <label v-if="type === 'api_key'" class="block text-sm">{{ t('marketplace.apiKey') }}<input v-model="apiKey" data-test="api-key" type="password" autocomplete="off" class="input mt-1 w-full" required /></label>
      <div v-if="type === 'oauth'" class="space-y-2 text-sm">
        <button type="button" class="btn-secondary" :disabled="busy || !provider || !accepted || !hasAgreement" @click="authorize">{{ t('marketplace.startAuthorization') }}</button>
        <p v-if="session">{{ t('marketplace.callbackHint') }}</p>
        <label v-if="session" class="block">{{ t('marketplace.callbackUrl') }}<input v-model="callback" class="input mt-1 w-full" autocomplete="off" /></label>
        <button v-if="session" type="button" class="btn-secondary" :disabled="busy" @click="complete">{{ t('marketplace.completeAuthorization') }}</button>
      </div>
      <p class="text-sm text-gray-600 dark:text-gray-300">{{ t('marketplace.policySummary', { fee: options.fee_bps, hours: options.hold_hours, version: options.agreement_version }) }}</p>
      <p v-if="options.agreement_text" class="whitespace-pre-wrap text-sm">{{ options.agreement_text }}</p><a v-if="agreementUrl" :href="agreementUrl" target="_blank" rel="noopener noreferrer" class="text-sm text-primary-600 underline">{{ t('marketplace.readAgreement') }}</a><p v-if="!hasAgreement" class="text-sm text-amber-600">{{ t('marketplace.agreementUnavailable') }}</p><RouterLink to="/available-channels" target="_blank" rel="noopener noreferrer" class="block text-sm text-primary-600 underline">{{ t('marketplace.viewPrices') }}</RouterLink><label class="flex items-start gap-2 text-sm"><input v-model="accepted" type="checkbox" />{{ t('marketplace.acceptAgreement') }}</label>
      <p v-if="error" role="alert" class="text-sm text-red-600">{{ error }}</p>
      <div class="flex justify-end gap-2"><button type="button" class="btn-secondary" @click="close">{{ t('common.cancel') }}</button><button v-if="type === 'api_key'" data-test="create" class="btn-primary" :disabled="busy || !valid">{{ t('marketplace.createDraft') }}</button></div>
    </form>
  </BaseDialog>
</template>
<script setup lang="ts">
import { computed, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import AccountConcurrencyForm from './AccountConcurrencyForm.vue'
import { createAccount, createAuthorization, completeAuthorization, getAccount } from '@/api/supplier'
import { useWorkModeStore } from '@/stores/workMode'
import type { SupplierOptions } from '@/types/marketplace'
const props = defineProps<{ options: SupplierOptions }>()
const emit = defineEmits<{ close: []; created: [id?: number] }>()
const { t } = useI18n(); const store = useWorkModeStore()
const accepted = ref(false); const platform = ref(props.options.providers[0]?.platform || ''); const type = ref('api_key'); const name = ref(''); const models = ref<string[]>([]); const concurrency = ref(1); const apiKey = ref(''); const busy = ref(false); const error = ref(''); const session = ref(''); const callback = ref('')
const agreementUrl = computed(() => { try { const url = new URL(props.options.agreement_url || ''); return ['http:', 'https:'].includes(url.protocol) ? url.href : '' } catch { return '' } }); const hasAgreement = computed(() => !!props.options.agreement_text?.trim() || !!agreementUrl.value)
const provider = computed(() => props.options.providers.find(p => p.platform === platform.value))
watch(provider, p => { type.value = p?.account_types[0] || ''; models.value = []; session.value = '' }, { immediate: true })
watch([name, models, concurrency, apiKey, accepted], () => { store.dirty = true }, { deep: true })
const valid = computed(() => !!provider.value && !!name.value.trim() && models.value.length > 0 && Number.isInteger(concurrency.value) && concurrency.value > 0 && concurrency.value <= props.options.max_supplier_concurrency && !!apiKey.value && accepted.value && hasAgreement.value)
async function close() { if (!await store.confirmLeave()) return; apiKey.value = ''; callback.value = ''; store.dirty = false; emit('close') }
onUnmounted(() => { apiKey.value = ''; callback.value = ''; store.dirty = false })
async function create() { if (!valid.value || busy.value) return; busy.value = true; error.value = ''; try { await createAccount({ name: name.value.trim(), platform: platform.value, type: type.value, api_key: apiKey.value, allowed_models: models.value, concurrency: concurrency.value }); apiKey.value = ''; store.dirty = false; emit('created') } catch { apiKey.value = ''; error.value = t('marketplace.authorizationFailed') } finally { busy.value = false } }
async function authorize() { if (!provider.value || !accepted.value || !hasAgreement.value || busy.value) return; busy.value = true; error.value = ''; try { const result = await createAuthorization({ purpose: 'create', provider: platform.value }); session.value = result.session_id; window.open(result.auth_url, '_blank', 'noopener,noreferrer') } catch { error.value = t('marketplace.authorizationFailed') } finally { busy.value = false } }
async function complete() { if (!session.value || busy.value) return; busy.value = true; error.value = ''; try { const url = new URL(callback.value); const state = url.searchParams.get('state'); const code = url.searchParams.get('code'); if (!state || !code) throw new Error('invalid callback'); const result = await completeAuthorization(session.value, { state, code, purpose: 'create' }); await getAccount(result.account_id); callback.value = ''; store.dirty = false; emit('created', result.account_id) } catch { callback.value = ''; error.value = t('marketplace.authorizationFailed') } finally { busy.value = false } }
</script>
