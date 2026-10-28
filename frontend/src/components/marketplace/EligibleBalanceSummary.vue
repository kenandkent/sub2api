<template>
  <section class="rounded-lg border border-teal-200 bg-teal-50 p-4 dark:border-teal-900 dark:bg-dark-800">
    <div class="flex flex-wrap items-center justify-between gap-2"><h2 class="font-semibold">{{ t('marketplace.eligibleBalance') }}</h2><button class="btn btn-secondary btn-sm" :disabled="loading" @click="$emit('refresh')">{{ t('common.refresh') }}</button></div>
    <p v-if="loading" class="mt-2 text-sm">{{ t('marketplace.loading') }}</p>
    <p v-else-if="error" role="alert" class="mt-2 text-sm text-red-700">{{ error }}</p>
    <template v-else-if="balance"><dl class="mt-3 grid grid-cols-2 gap-3 sm:grid-cols-4"><div v-for="item in amounts" :key="item.label"><dt class="text-sm text-gray-600 dark:text-gray-400">{{ t(item.label) }}</dt><dd class="font-medium tabular-nums">{{ balance.currency }} {{ item.value }}</dd></div></dl><p class="mt-3 text-sm text-gray-600 dark:text-gray-400">{{ t('marketplace.excludedExplanation') }}</p><p v-if="!balance.migrated || !balance.reconciled" class="mt-2 text-sm">{{ t('marketplace.balancePending') }}</p></template>
  </section>
</template>
<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { MarketplaceBalance } from '@/api/marketplace'
const props = defineProps<{ balance: MarketplaceBalance | null; loading: boolean; error: string }>()
defineEmits<{ refresh: [] }>()
const { t } = useI18n()
const amounts = computed(() => [{ label: 'marketplace.eligibleAvailable', value: props.balance?.eligible_available }, { label: 'marketplace.reservedBalance', value: props.balance?.reserved }, { label: 'marketplace.excludedBalance', value: props.balance?.excluded_available }, { label: 'marketplace.debtBalance', value: props.balance?.debt }])
</script>
