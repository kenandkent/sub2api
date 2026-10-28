<template>
  <div ref="root" class="relative px-3 pb-2" @keydown.esc="closeMenu">
    <button ref="trigger" type="button" class="sidebar-link w-full" :class="{ 'justify-center': collapsed }"
      :aria-label="t('marketplace.switchWorkspace')" :aria-expanded="open" @click="open = !open">
      <Icon name="grid" size="md" />
      <span v-if="!collapsed" class="min-w-0 flex-1 truncate text-left">{{ t(`marketplace.${workMode.mode}`) }}</span>
      <Icon v-if="!collapsed" name="chevronDown" size="sm" />
    </button>
    <div v-if="open" class="absolute left-3 right-3 z-40 mt-1 rounded-lg border border-gray-200 bg-white p-1 shadow-lg dark:border-dark-700 dark:bg-dark-800" :class="{ 'w-48': collapsed }">
      <button v-for="item in modes" :key="item" type="button" :data-mode="item" class="dropdown-item w-full justify-between" @click="choose(item)">
        {{ t(`marketplace.${item}`) }} <span v-if="workMode.mode === item" aria-hidden="true">✓</span>
      </button>
    </div>
  </div>
</template>
<script setup lang="ts">
import { ref, onMounted, onBeforeUnmount } from 'vue'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { useAuthStore } from '@/stores/auth'
import { useAppStore } from '@/stores/app'
import { useWorkModeStore } from '@/stores/workMode'
import type { WorkMode } from '@/types/marketplace'
import Icon from '@/components/icons/Icon.vue'

defineProps<{ collapsed: boolean }>()
const { t } = useI18n()
const auth = useAuthStore()
const app = useAppStore()
const workMode = useWorkModeStore()
const router = useRouter()
const modes: WorkMode[] = ['consumer', 'supplier']
const open = ref(false)
const root = ref<HTMLElement | null>(null)
const trigger = ref<HTMLButtonElement | null>(null)
function closeMenu() { open.value = false; trigger.value?.focus() }
function choose(mode: WorkMode) {
  open.value = false
  void switchTo(mode)
}
async function switchTo(mode: WorkMode) {
  const userId = auth.user?.id
  if (userId == null) return
  await workMode.loadCapabilities(userId)
  if (auth.user?.id !== userId) return
  const result = await router.push(workMode.targetFor(mode))
  if (!result && auth.user?.id === userId) { workMode.setPreference(userId, mode); app.setMobileOpen(false) }
}
function outside(event: MouseEvent) { if (!root.value?.contains(event.target as Node)) open.value = false }
onMounted(() => document.addEventListener('click', outside))
onBeforeUnmount(() => document.removeEventListener('click', outside))
</script>
