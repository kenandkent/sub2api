<template>
  <AppLayout><TablePageLayout>
    <template #actions><div class="flex flex-wrap items-center justify-between gap-3"><div><h1 class="text-xl font-semibold">{{ t('marketplace.accounts') }}</h1><p class="mt-1 text-sm text-gray-500">{{ t('marketplace.accountsDescription') }}</p></div><button class="btn-primary" :disabled="!canCreate" @click="wizard = true">{{ t('marketplace.addAccount') }}</button></div><p v-if="!options && !loading" class="mt-2 text-sm text-amber-600">{{ t('marketplace.optionsUnavailable') }}</p></template>
    <template #filters><form data-test="apply-filters" class="grid gap-3 sm:grid-cols-2 lg:grid-cols-5" @submit.prevent="applyFilters"><label class="text-sm">{{ t('marketplace.searchAccounts') }}<input v-model="filters.q" data-test="filter-q" class="input mt-1 w-full" /></label><label class="text-sm">{{ t('marketplace.provider') }}<input v-model="filters.platform" data-test="filter-platform" class="input mt-1 w-full" /></label><label class="text-sm">{{ t('marketplace.reviewStatus') }}<select v-model="filters.status" data-test="filter-status" class="input mt-1 w-full"><option value="">{{ t('marketplace.allStatuses') }}</option><option v-for="status in reviewStatuses" :key="status" :value="status">{{ t(`marketplace.review_${status}`) }}</option></select></label><label class="text-sm">{{ t('marketplace.allowedModels') }}<input v-model="filters.model" data-test="filter-model" class="input mt-1 w-full" /></label><button class="btn-secondary self-end">{{ t('marketplace.applyFilters') }}</button></form></template>
    <template #table><div v-if="error" role="alert" class="p-6 text-sm text-red-600">{{ t('marketplace.loadFailed') }} <button class="underline" @click="load">{{ t('marketplace.retry') }}</button></div><DataTable v-else :columns="columns" :data="accounts" :loading="loading">
      <template #empty><div class="p-8 text-center text-sm text-gray-500">{{ t('marketplace.noAccounts') }}</div></template>
      <template #cell-name="{ row }"><RouterLink class="text-primary-600 hover:underline" :to="`/supplier/accounts/${row.id}`">{{ row.name }}</RouterLink></template>
      <template #cell-models="{ row }">{{ row.offer.allowed_models.join(', ') }}</template>
      <template #cell-concurrency="{ row }">{{ row.current_concurrency }} / {{ row.concurrency }}</template>
      <template #cell-review="{ row }"><SupplierStatusBadge kind="review" :status="row.offer.review_status" /></template>
      <template #cell-availability="{ row }">{{ t(row.offer.enabled ? 'marketplace.enabled' : 'marketplace.disabled') }}</template>
      <template #cell-health="{ row }"><SupplierStatusBadge kind="credential" :status="row.credential_status" /></template>
      <template #cell-actions="{ row }"><div class="flex flex-wrap gap-2"><RouterLink class="text-primary-600" :to="`/supplier/accounts/${row.id}`">{{ t('marketplace.details') }}</RouterLink><button v-if="row.offer.enabled" data-test="pause" class="text-amber-600" :disabled="busy" @click="confirm(row, 'pause')">{{ t('marketplace.pause') }}</button><button v-else-if="row.offer.review_status === 'approved' && !readOnly" class="text-primary-600" :disabled="busy" @click="confirm(row, 'resume')">{{ t('marketplace.resume') }}</button></div></template>
    </DataTable><div v-if="pages > 1" class="flex items-center justify-center gap-4 p-4 text-sm"><button :disabled="page <= 1" @click="page--; load()">{{ t('marketplace.previous') }}</button><span>{{ page }} / {{ pages }}</span><button :disabled="page >= pages" @click="page++; load()">{{ t('marketplace.next') }}</button></div></template>
  </TablePageLayout><AccountShareWizard v-if="wizard && options" :options="options" @close="wizard = false" @created="created" /><ConfirmDialog :show="!!action" :title="t(`marketplace.${action?.kind || 'pause'}`)" :message="t(action?.kind === 'pause' ? 'marketplace.pauseExplanation' : 'marketplace.resumeExplanation')" @cancel="action = null" @confirm="perform" /></AppLayout>
</template>
<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import AppLayout from '@/components/layout/AppLayout.vue'
import TablePageLayout from '@/components/layout/TablePageLayout.vue'
import DataTable from '@/components/common/DataTable.vue'
import SupplierStatusBadge from '@/components/supplier/SupplierStatusBadge.vue'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import AccountShareWizard from '@/components/supplier/AccountShareWizard.vue'
import { listAccounts, getSupplierOptions, accountAction } from '@/api/supplier'
import { useWorkModeStore } from '@/stores/workMode'
import type { SupplierAccount, SupplierOptions, SupplierAccountFilters } from '@/types/marketplace'
const { t } = useI18n(); const router = useRouter(); const store = useWorkModeStore(); const accounts = ref<SupplierAccount[]>([]); const options = ref<SupplierOptions | null>(null); const loading = ref(true); const error = ref(false); const busy = ref(false); const wizard = ref(false); const page = ref(1); const pages = ref(1); const action = ref<{ row: SupplierAccount; kind: 'pause' | 'resume' } | null>(null)
const reviewStatuses = ['draft', 'pending_review', 'approved', 'rejected', 'revoked']
const filters = ref<SupplierAccountFilters>({ q: '', platform: '', status: '', model: '' }); let activeFilters: SupplierAccountFilters = { ...filters.value }; let generation = 0
const readOnly = computed(() => store.capabilities?.supplier_status === 'suspended')
const canCreate = computed(() => !readOnly.value && !!options.value?.providers?.length && options.value.max_supplier_concurrency > 0)
const columns = computed(() => [{ key: 'name', label: t('marketplace.accountName') }, { key: 'platform', label: t('marketplace.provider') }, { key: 'models', label: t('marketplace.allowedModels') }, { key: 'concurrency', label: t('marketplace.concurrency') }, { key: 'review', label: t('marketplace.reviewStatus') }, { key: 'health', label: t('marketplace.healthStatus') }, { key: 'availability', label: t('marketplace.availability') }, { key: 'actions', label: t('common.actions') }])
async function load() { const sequence = ++generation; loading.value = true; error.value = false; try { const [result, config] = await Promise.all([listAccounts(page.value, activeFilters), getSupplierOptions().catch(() => null)]); if (sequence !== generation) return; accounts.value = result.items; pages.value = result.pages || 1; options.value = config } catch { if (sequence === generation) error.value = true } finally { if (sequence === generation) loading.value = false } }
function applyFilters() { activeFilters = { ...filters.value }; page.value = 1; void load() }
function created(id?: number) { wizard.value = false; if (id) void router.push(`/supplier/accounts/${id}`); else void load() }
function confirm(row: SupplierAccount, kind: 'pause' | 'resume') { action.value = { row, kind } }
async function perform() { if (!action.value || busy.value) return; busy.value = true; try { await accountAction(action.value.row.id, action.value.kind, action.value.row.offer.version); action.value = null; await load() } catch { action.value = null; error.value = true } finally { busy.value = false } }
onMounted(load)
</script>
