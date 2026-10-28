<template>
  <AppLayout><TablePageLayout>
    <template #filters><div class="flex flex-wrap items-center justify-between gap-4"><div><h1 class="text-2xl font-semibold">{{ t(`adminMarketplace.${kind}`) }}</h1><p class="mt-1 text-sm text-gray-500">{{ t('adminMarketplace.reviewHint') }}</p></div><button class="btn btn-secondary" :disabled="loading" @click="load">{{ t('common.refresh') }}</button></div><p v-if="error" role="alert" class="mt-3 text-red-600">{{ error }} <button class="underline" @click="load">{{ t('adminMarketplace.retry') }}</button></p></template>
    <template #table><DataTable :columns="columns" :data="rows" :loading="loading"><template #cell-details="{ row }"><span v-if="kind === 'suppliers'">{{ Object.entries(row.application_fields || {}).map(([k,v]) => `${k}: ${v}`).join(', ') }} · {{ t('adminMarketplace.agreement') }} {{ row.agreement_version }} · {{ row.payout_frozen ? t('adminMarketplace.frozen') : t('adminMarketplace.notFrozen') }}<span v-if="row.review_reason"> · {{ row.review_reason }}</span></span><span v-else>{{ row.platform }} / {{ row.type }} · {{ row.concurrency }} · {{ (row.allowed_models || []).join(', ') }} · {{ row.enabled ? t('adminMarketplace.enabled') : t('adminMarketplace.disabled') }}</span></template><template #cell-actions="{ row }"><button class="btn btn-secondary btn-sm" @click="open(row)">{{ t('adminMarketplace.review') }}</button></template></DataTable></template>
    <template #pagination><Pagination v-if="total > 20" :page="page" :page-size="20" :total="total" @update:page="changePage" /></template>
  </TablePageLayout>
  <BaseDialog :show="!!selected" :title="t('adminMarketplace.review')" @close="selected = null"><div class="space-y-4"><p>{{ t('adminMarketplace.version') }} {{ selected?.version }} · #{{ kind === 'suppliers' ? selected?.user_id : selected?.id }}</p><label class="block">{{ t('adminMarketplace.decision') }}<select v-model="decision" class="input mt-1"><option v-for="choice in choices" :key="choice" :value="choice">{{ t(`adminMarketplace.${choice}`) }}</option></select></label><label class="block">{{ t('adminMarketplace.reason') }}<textarea v-model="reason" class="input mt-1" rows="3" required /></label><p v-if="dialogError" role="alert" class="text-red-600">{{ dialogError }} <button v-if="conflict" class="underline" @click="reloadSelected">{{ t('adminMarketplace.reloadLatest') }}</button></p></div><template #footer><button class="btn btn-secondary" @click="selected = null">{{ t('common.cancel') }}</button><button class="btn btn-primary" :disabled="busy || !reason.trim()" @click="submit">{{ t('adminMarketplace.confirmReview') }}</button></template></BaseDialog>
  </AppLayout>
</template>
<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import TablePageLayout from '@/components/layout/TablePageLayout.vue'
import DataTable from '@/components/common/DataTable.vue'
import Pagination from '@/components/common/Pagination.vue'
import BaseDialog from '@/components/common/BaseDialog.vue'
import { items, list, post, type Supplier, type Offer } from '@/api/admin/marketplace'
const props = defineProps<{ kind: 'suppliers' | 'offers' }>()
const { t } = useI18n()
const loading = ref(false), busy = ref(false), conflict = ref(false), error = ref(''), dialogError = ref(''), page = ref(1), total = ref(0)
const rows = ref<(Supplier | Offer)[]>([]), selected = ref<any>(null), decision = ref(''), reason = ref('')
const choices = computed(() => props.kind === 'suppliers' ? ['active', 'rejected', 'suspended'] : ['approved', 'rejected'])
const columns = computed(() => [{ key: props.kind === 'suppliers' ? 'user_id' : 'id', label: t('adminMarketplace.id') }, { key: props.kind === 'suppliers' ? 'status' : 'review_status', label: t('adminMarketplace.status') }, ...(props.kind === 'offers' ? [{ key: 'account_name', label: t('adminMarketplace.account') }, { key: 'owner_user_id', label: t('adminMarketplace.supplier') }] : []), { key: 'details', label: t('adminMarketplace.details') }, { key: 'actions', label: t('adminMarketplace.actions') }])
async function load() { loading.value = true; error.value = ''; try { const result = await list<Supplier | Offer>(props.kind, page.value); rows.value = items(result); total.value = Array.isArray(result) ? result.length : result.total } catch (e: any) { error.value = e?.message || t('adminMarketplace.loadError') } finally { loading.value = false } }
function changePage(next: number) { page.value = next; void load() }
async function reloadSelected() { const id = props.kind === 'suppliers' ? selected.value?.user_id : selected.value?.id; await load(); const latest = rows.value.find((row: any) => (props.kind === 'suppliers' ? row.user_id : row.id) === id); if (latest) { selected.value = latest; dialogError.value = ''; conflict.value = false } else dialogError.value = error.value || t('adminMarketplace.loadError') }
function open(row: Supplier | Offer) { conflict.value = false; selected.value = row; decision.value = choices.value[0]; reason.value = ''; dialogError.value = '' }
async function submit() { if (!selected.value || !reason.value.trim() || busy.value) return; busy.value = true; dialogError.value = ''; try { const id = props.kind === 'suppliers' ? selected.value.user_id : selected.value.id; await post(`${props.kind}/${id}/review`, props.kind === 'suppliers' ? { status: decision.value, reason: reason.value } : { status: decision.value, reason: reason.value, expected_version: selected.value.version }); selected.value = null; await load() } catch (e: any) { conflict.value = e?.status === 409; dialogError.value = conflict.value ? t('adminMarketplace.versionConflict') : e?.message || t('adminMarketplace.actionError') } finally { busy.value = false } }
onMounted(load)
</script>
