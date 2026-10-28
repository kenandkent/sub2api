<template><StatusBadge :status="variant" :label="t(`marketplace.${kind}_${known ? status : 'unknown'}`)" /></template>
<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import StatusBadge from '@/components/common/StatusBadge.vue'
const props = defineProps<{ kind: 'review' | 'credential'; status: string }>()
const { t } = useI18n()
const statuses = { review: ['draft', 'pending_review', 'approved', 'rejected', 'revoked'], credential: ['valid', 'invalid', 'expired', 'unknown', 'credentials_invalid', 'active', 'pending'] }
const known = computed(() => statuses[props.kind].includes(props.status))
const variant = computed(() => ['approved', 'valid', 'active'].includes(props.status) ? 'success' : ['rejected', 'revoked', 'invalid', 'expired', 'credentials_invalid'].includes(props.status) ? 'danger' : 'warning')
</script>
