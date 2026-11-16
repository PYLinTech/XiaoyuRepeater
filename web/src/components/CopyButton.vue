<script setup lang="ts">
import Icon from './Icon.vue'
import { copyText } from '../clipboard'
import { toast } from '../toast'

withDefaults(
  defineProps<{
    value: string
    label?: string
    iconOnly?: boolean
  }>(),
  { label: '复制', iconOnly: false },
)

async function handleCopy(value: string) {
  const ok = await copyText(value)
  if (ok) toast('已复制', 'success')
  else toast('复制失败，请手动选择内容', 'error')
}
</script>

<template>
  <button
    type="button"
    class="copy-btn"
    :class="{ 'btn': !iconOnly, 'btn-ghost': !iconOnly, 'btn-sm': !iconOnly, 'icon-btn': iconOnly }"
    :title="label"
    :aria-label="label"
    @click.stop="handleCopy(value)"
  >
    <Icon name="copy" :size="iconOnly ? 15 : 14" />
    <span v-if="!iconOnly">{{ label }}</span>
  </button>
</template>
