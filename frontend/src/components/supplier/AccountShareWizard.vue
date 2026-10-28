<template>
  <BaseDialog :show="true" :z-index="40" :title="t('marketplace.addAccount')" @close="close">
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
        <p v-if="options.agreement_text" class="whitespace-pre-wrap text-sm">{{ options.agreement_text }}</p>
        <a v-if="agreementUrl" :href="agreementUrl" target="_blank" rel="noopener noreferrer" class="text-sm text-primary-600 underline">{{ t('marketplace.readAgreement') }}</a>
        <p v-if="!hasAgreement" class="text-sm text-amber-600">{{ t('marketplace.agreementUnavailable') }}</p>
        <RouterLink to="/available-channels" target="_blank" rel="noopener noreferrer" class="block text-sm text-primary-600 underline">{{ t('marketplace.viewPrices') }}</RouterLink>
        <label class="flex items-start gap-2 text-sm"><input v-model="accepted" type="checkbox" />{{ t('marketplace.acceptAgreement') }}</label>
        <div v-if="type === 'oauth' && session && !authorizedId" class="space-y-2 text-sm"><a :href="authUrl" data-test="authorization-link" target="_blank" rel="noopener noreferrer" class="text-primary-600 underline">{{ t('marketplace.openAuthorization') }}</a><p>{{ t('marketplace.callbackHint') }}</p><label class="block">{{ t('marketplace.callbackUrl') }}<input v-model="callback" class="input mt-1 w-full" autocomplete="off" /></label></div>
      </div>
      <p v-if="error" role="alert" class="text-sm text-red-600">{{ error }}</p><button v-if="authorizedId && error" type="button" class="text-sm text-primary-600 underline" @click="openCreated">{{ t('marketplace.openCreatedAccount') }}</button>
      <div class="flex justify-end gap-2">
        <button type="button" class="btn-secondary" @click="close">{{ t('common.cancel') }}</button>
        <button v-if="step > 1" type="button" data-test="back" class="btn-secondary" :disabled="busy || !!authorizedId" @click="step--">{{ t('marketplace.previous') }}</button>
        <button v-if="step < 3" type="button" data-test="next" class="btn-primary" :disabled="busy || !canAdvance" @click="step++">{{ t('marketplace.next') }}</button>
        <button v-else data-test="create" class="btn-primary" :disabled="busy || !accepted || !hasAgreement">{{ type === 'oauth' && authorizedId ? t('marketplace.save') : type === 'oauth' && !session ? t('marketplace.startAuthorization') : type === 'oauth' ? t('marketplace.completeAuthorization') : t('marketplace.createDraft') }}</button>
      </div>
    </form>
  </BaseDialog>
</template>
<script setup lang="ts">
import { computed, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import AccountConcurrencyForm from './AccountConcurrencyForm.vue'
import { createAccount, createAuthorization, completeAuthorization, getAccount, updateAccount } from '@/api/supplier'
import { useWorkModeStore } from '@/stores/workMode'
import type { SupplierOptions } from '@/types/marketplace'
const props = defineProps<{ options: SupplierOptions }>()
const emit = defineEmits<{ close: []; created: [id?: number] }>()
const { t } = useI18n(); const store = useWorkModeStore()
const step = ref(1); const accepted = ref(false); const platform = ref(props.options.providers[0]?.platform || ''); const type = ref('api_key'); const name = ref(''); const models = ref<string[]>([]); const concurrency = ref(1); const apiKey = ref(''); const busy = ref(false); const error = ref(''); const session = ref(''); const authUrl = ref(''); const callback = ref(''); const authorizedId = ref<number | null>(null)
const agreementUrl = computed(() => { try { const url = new URL(props.options.agreement_url || ''); return ['http:', 'https:'].includes(url.protocol) ? url.href : '' } catch { return '' } }); const hasAgreement = computed(() => !!props.options.agreement_text?.trim() || !!agreementUrl.value)
const provider = computed(() => props.options.providers.find(p => p.platform === platform.value))
watch(provider, p => { type.value = p?.account_types[0] || ''; models.value = []; session.value = ''; authUrl.value = ''; apiKey.value = '' }, { immediate: true })
watch(type, () => { session.value = ''; authUrl.value = ''; callback.value = ''; apiKey.value = '' })
watch([name, models, concurrency, apiKey, accepted, platform, type], () => { store.dirty = true }, { deep: true })
const configValid = computed(() => !!name.value.trim() && models.value.length > 0 && Number.isInteger(concurrency.value) && concurrency.value > 0 && concurrency.value <= props.options.max_supplier_concurrency)
const canAdvance = computed(() => step.value === 1 ? !!provider.value && !!type.value && (type.value !== 'api_key' || !!apiKey.value) : configValid.value)
function openCreated() { if (!authorizedId.value) return; callback.value = ''; store.dirty = false; emit('created', authorizedId.value) }
async function close() { if (!await store.confirmLeave()) return; apiKey.value = ''; callback.value = ''; store.dirty = false; emit('close') }
onUnmounted(() => { apiKey.value = ''; callback.value = ''; store.dirty = false })
async function submit() {
  if (step.value !== 3 || !configValid.value || !accepted.value || !hasAgreement.value || busy.value) return
  busy.value = true; error.value = ''
  try {
    if (type.value === 'api_key') {
      if (!apiKey.value) { step.value = 1; return }
      await createAccount({ name: name.value.trim(), platform: platform.value, type: type.value, api_key: apiKey.value, allowed_models: models.value, concurrency: concurrency.value })
      apiKey.value = ''; store.dirty = false; emit('created'); return
    }
    if (!session.value && !authorizedId.value) {
      const result = await createAuthorization({ purpose: 'create', provider: platform.value }); const url = new URL(result.auth_url); if (!['http:', 'https:'].includes(url.protocol)) throw new Error('invalid authorization URL'); authUrl.value = url.href; session.value = result.session_id; return
    }
    if (!authorizedId.value) {
      const url = new URL(callback.value); const state = url.searchParams.get('state'); const code = url.searchParams.get('code'); if (!state || !code) throw new Error('invalid callback')
      authorizedId.value = (await completeAuthorization(session.value, { state, code, purpose: 'create' })).account_id
      callback.value = ''
    }
    const account = await getAccount(authorizedId.value)
    await updateAccount(account.id, { expected_version: account.offer.version, name: name.value.trim(), concurrency: concurrency.value, allowed_models: models.value })
    store.dirty = false; emit('created', account.id)
  } catch (e) {
    apiKey.value = ''; callback.value = ''
    error.value = (e as { status?: number }).status === 409 ? t('marketplace.versionConflict') : t('marketplace.authorizationFailed')
  } finally { busy.value = false }
}
</script>
