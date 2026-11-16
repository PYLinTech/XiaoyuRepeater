<script setup lang="ts">
import Icon from './Icon.vue'

withDefaults(
  defineProps<{
    title: string
    description?: string
    icon?: string
  }>(),
  { description: '', icon: 'server' },
)
</script>

<template>
  <div class="empty">
    <div class="empty-icon">
      <Icon :name="icon" :size="20" />
    </div>
    <div class="empty-title">{{ title }}</div>
    <p v-if="description" class="empty-desc">{{ description }}</p>
    <div v-if="$slots.default" class="empty-action">
      <slot />
    </div>
  </div>
</template>

<style scoped>
.empty-action {
  margin-top: 8px;
}
</style>
