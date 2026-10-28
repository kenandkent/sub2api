<template><AppLayout><div class="card max-w-2xl p-6"><h2 class="text-lg font-semibold text-gray-900 dark:text-white">{{ t('marketplace.overview') }}</h2><p class="mt-2 text-sm text-gray-500 dark:text-dark-400">{{ t('marketplace.overviewDescription') }}</p></div></AppLayout></template>
<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
const { t } = useI18n()
</script>
