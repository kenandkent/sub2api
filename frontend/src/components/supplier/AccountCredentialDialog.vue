<template>
  <BaseDialog :show="true" :z-index="40" :title="t('marketplace.replaceCredentials')" :close-on-escape="!locked" :close-on-click-outside="!locked" :show-close-button="!locked" @close="close">
    <form class="space-y-4" @submit.prevent="submit">
      <p class="text-sm text-gray-500">{{ t('marketplace.reauthorizationReview') }}</p>
      <label v-if="account.type === 'api_key' && !unknown" class="block text-sm">{{ t('marketplace.apiKey') }}<input v-model="key" data-test="credential-key" type="password" autocomplete="off" class="input mt-1 w-full" /></label>
      <template v-if="account.type === 'oauth' && session && !unknown"><a :href="authUrl" target="_blank" rel="noopener noreferrer" class="text-primary-600 underline">{{ t('marketplace.openAuthorization') }}</a><label class="block text-sm">{{ t('marketplace.callbackUrl') }}<input v-model="callback" data-test="credential-callback" autocomplete="off" class="input mt-1 w-full" /></label></template>
      <p v-if="unknown" role="alert" class="text-sm text-amber-600">{{ t(unknown === 'authorization' ? 'marketplace.authorizationUnknown' : 'marketplace.operationUnknown') }}</p>
      <button v-if="unknown" type="button" data-test="check-credential" class="btn-secondary" :disabled="busy" @click="check">{{ t('marketplace.checkStatus') }}</button>
      <p v-if="error" role="alert" class="text-sm text-red-600">{{ error }}</p>
      <div class="flex justify-end gap-2"><button type="button" class="btn-secondary" :disabled="locked" @click="close">{{ t('common.cancel') }}</button><button class="btn-primary" :disabled="locked || (account.type === 'api_key' && !key)">{{ account.type === 'api_key' ? t('marketplace.replaceCredentials') : session ? t('marketplace.completeAuthorization') : t('marketplace.startAuthorization') }}</button></div>
    </form>
  </BaseDialog>
</template>
<script setup lang="ts">
import { computed, onUnmounted, ref } from 'vue'
import { onBeforeRouteLeave, onBeforeRouteUpdate } from 'vue-router'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import { replaceCredentials, getSupplierOperation, createAuthorization, completeAuthorization, getAuthorizationStatus } from '@/api/supplier'
import type { SupplierAccount } from '@/types/marketplace'
const props = defineProps<{ account: SupplierAccount }>(); const emit = defineEmits<{ close: []; saved: [] }>(); const { t } = useI18n()
const key = ref(''); const busy = ref(false); const unknown = ref<'' | 'authorization' | 'credential'>(''); const session = ref(''); const authUrl = ref(''); const callback = ref(''); const operationKey = ref(''); const error = ref('')
const locked = computed(() => busy.value || !!unknown.value)
onBeforeRouteLeave(() => !locked.value)
onBeforeRouteUpdate(() => !locked.value)
function beforeUnload(event: BeforeUnloadEvent) { if (locked.value) { event.preventDefault(); event.returnValue = '' } }
window.addEventListener('beforeunload', beforeUnload)
onUnmounted(() => { window.removeEventListener('beforeunload', beforeUnload); key.value = ''; callback.value = '' })
function close() { if (!locked.value) { key.value = ''; callback.value = ''; emit('close') } }
async function submit() {
  if (locked.value) return; busy.value = true; error.value = ''; let phase: 'key' | 'start' | 'complete' | 'local' = 'local'
  try {
    if (props.account.type === 'api_key') { if (!key.value) return; operationKey.value ||= crypto.randomUUID(); phase = 'key'; await replaceCredentials(props.account.id, { api_key: key.value, expected_version: props.account.offer.version }, operationKey.value); key.value = ''; busy.value = false; emit('saved'); return }
    if (!session.value) { phase = 'start'; const result = await createAuthorization({ purpose: 'reauthorize', provider: props.account.platform, account_id: props.account.id }); const url = new URL(result.auth_url); if (!['http:', 'https:'].includes(url.protocol)) throw new Error('invalid authorization URL'); session.value = result.session_id; authUrl.value = url.href; return }
    const url = new URL(callback.value); const state = url.searchParams.get('state'); const code = url.searchParams.get('code'); if (!state || !code) throw new Error('invalid callback')
    phase = 'complete'; const result = await completeAuthorization(session.value, { state, code, purpose: 'reauthorize', account_id: props.account.id }); if (result.account_id !== props.account.id) throw new Error('unexpected account'); callback.value = ''; busy.value = false; emit('saved')
  } catch (e) {
    key.value = ''; callback.value = ''; const status = (e as { status?: number }).status
    if (phase === 'complete' && ![400, 401, 403, 404, 410].includes(status || 0)) unknown.value = 'authorization'
    else if (phase === 'key' && ![400, 401, 403, 404, 409, 410, 422].includes(status || 0)) unknown.value = 'credential'
    else { if (phase === 'complete') { session.value = ''; authUrl.value = '' }; if (phase === 'key') operationKey.value = ''; error.value = status === 409 ? t('marketplace.versionConflict') : t('marketplace.authorizationFailed') }
  } finally { busy.value = false }
  if (unknown.value === 'authorization') await check()
}
async function check() { if (busy.value) return; busy.value = true; try { const result = unknown.value === 'authorization' ? await getAuthorizationStatus(session.value) : await getSupplierOperation(operationKey.value); if (result.status === 'completed' && (result.account_id === undefined || result.account_id === props.account.id)) { unknown.value = ''; busy.value = false; emit('saved') } else if (result.status === 'failed') { unknown.value = ''; session.value = ''; authUrl.value = ''; operationKey.value = ''; error.value = t('marketplace.authorizationFailed') } } catch { /* Keep the unknown state until the server confirms a result. */ } finally { busy.value = false } }
</script>
