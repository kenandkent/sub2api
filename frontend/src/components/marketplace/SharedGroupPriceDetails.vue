<template>
  <section v-if="groups.length" class="rounded-lg border border-gray-200 bg-white p-4 dark:border-dark-600 dark:bg-dark-800">
    <h2 class="font-semibold text-gray-900 dark:text-white">{{ t('marketplace.sharedPrices') }}</h2>
    <p class="mt-1 text-sm text-gray-500">{{ t('marketplace.pricesPerMillion') }}</p>
    <details v-for="group in groups" :key="group.group_id" class="mt-3 border-t border-gray-100 pt-3 dark:border-dark-600">
      <summary class="cursor-pointer font-medium">{{ group.name }} <span class="text-xs text-teal-700">{{ t('marketplace.sharedGroup') }}</span></summary>
      <div v-for="model in group.models" :key="model.public_model" class="mt-3">
        <div class="font-medium">{{ model.public_model }} <span class="text-sm font-normal text-gray-500">{{ t('marketplace.rateMultiplier') }}: {{ model.rate_multiplier }}</span></div>
        <p class="text-xs text-gray-500">{{ t('marketplace.contextLimit') }}: {{ model.context_limit }} · {{ t('marketplace.maxOutput') }}: {{ model.max_output_tokens }}</p>
        <p v-if="!model.tiers?.length" class="mt-2 text-sm text-gray-500">{{ t('marketplace.priceMissing') }}</p>
        <div v-else class="overflow-x-auto"><table class="mt-2 w-full text-left text-sm tabular-nums"><thead><tr><th>{{ t('marketplace.fromTokens') }}</th><th>{{ t('marketplace.inputPrice') }}</th><th>{{ t('marketplace.outputPrice') }}</th><th>{{ t('marketplace.cacheReadPrice') }}</th><th>{{ t('marketplace.cacheWritePrice') }}</th></tr></thead><tbody><tr v-for="tier in model.tiers" :key="tier.from_tokens"><td>{{ tier.from_tokens }}</td><td>{{ price(tier.input_per_million) }}</td><td>{{ price(tier.output_per_million) }}</td><td>{{ price(tier.cache_read_per_million) }}</td><td>{{ price(tier.cache_write_per_million) }}</td></tr></tbody></table></div>
      </div>
    </details>
  </section>
</template>
<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import type { CatalogGroup } from '@/api/marketplace'
defineProps<{ groups: CatalogGroup[] }>()
const { t } = useI18n()
const price = (value: string | null | undefined) => value === '' || value == null ? t('marketplace.priceMissing') : `$${value}`
</script>
