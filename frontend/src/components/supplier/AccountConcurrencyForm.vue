<template>
  <label class="block text-sm font-medium text-gray-700 dark:text-gray-200">
    {{ t('marketplace.concurrency') }}
    <input :value="modelValue" type="number" min="1" :max="max" :disabled="disabled" required class="input mt-1 w-full" @input="$emit('update:modelValue', Number(($event.target as HTMLInputElement).value))" />
  </label>
  <p class="mt-1 text-xs text-gray-500 dark:text-dark-400">{{ t('marketplace.concurrencyHint') }} {{ t('marketplace.concurrencyReduction') }}</p>
</template>
<script setup lang="ts">
import { useI18n } from 'vue-i18n'
defineProps<{ modelValue: number; max: number; disabled?: boolean }>()
defineEmits<{ 'update:modelValue': [value: number] }>()
const { t } = useI18n()
</script>
