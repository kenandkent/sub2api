<template>
  <AppLayout>
    <div class="card max-w-2xl p-6">
      <h2 class="text-lg font-semibold text-gray-900 dark:text-white">{{ t('marketplace.onboarding') }}</h2>
      <p class="mt-2 text-sm text-gray-500 dark:text-dark-400">{{ t('marketplace.onboardingDescription') }}</p>
      <p v-if="store.capabilities" class="mt-6 text-sm text-gray-700 dark:text-dark-200">{{ t('marketplace.status') }}: {{ t(`marketplace.${store.capabilities.supplier_status}`) }}</p>
      <div v-else class="mt-6 text-sm text-gray-600 dark:text-dark-300">
        <p>{{ t('marketplace.unavailable') }}</p>
        <button class="btn-secondary mt-3" type="button" @click="retry">{{ t('marketplace.retry') }}</button>
      </div>
    </div>
  </AppLayout>
</template>
<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import { useWorkModeStore } from '@/stores/workMode'
import { useAuthStore } from '@/stores/auth'
const { t } = useI18n()
const store = useWorkModeStore()
const auth = useAuthStore()
function retry() { if (auth.user) void store.loadCapabilities(auth.user.id) }
</script>
