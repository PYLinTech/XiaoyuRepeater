<script setup lang="ts">
import { computed } from 'vue'

const props = withDefaults(
  defineProps<{
    name: string
    size?: number
  }>(),
  { size: 18 },
)

const ICONS: Record<string, string> = {
  overview: 'ri-layout-grid-line',
  rooms: 'ri-rss-line',
  keys: 'ri-key-2-line',
  copy: 'ri-file-copy-line',
  plus: 'ri-add-line',
  close: 'ri-close-line',
  search: 'ri-search-line',
  'chevron-right': 'ri-arrow-right-s-line',
  logout: 'ri-logout-box-r-line',
  lock: 'ri-lock-line',
  eye: 'ri-eye-line',
  'eye-off': 'ri-eye-off-line',
  activity: 'ri-pulse-line',
  users: 'ri-group-line',
  message: 'ri-message-3-line',
  server: 'ri-server-line',
  check: 'ri-check-line',
  alert: 'ri-error-warning-line',
  info: 'ri-information-line',
  refresh: 'ri-refresh-line',
  trash: 'ri-delete-bin-line',
  sdk: 'ri-code-s-slash-line',
  download: 'ri-download-2-line',
  archive: 'ri-file-zip-line',
  code: 'ri-code-box-line',
  javascript: 'ri-javascript-line',
  terminal: 'ri-terminal-box-line',
}

const iconClass = computed(() => ICONS[props.name] ?? 'ri-question-line')
</script>

<template>
  <i
    :class="[iconClass, 'ri-icon']"
    :style="{ fontSize: `${size}px` }"
    aria-hidden="true"
  />
</template>

<style scoped>
.ri-icon {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  flex-shrink: 0;
  width: 1em;
  height: 1em;
  line-height: 1;
}
</style>
