<script setup lang="ts">
defineProps<{
  modelValue: string
  options: { value: string; label: string; count?: number }[]
}>()

const emit = defineEmits<{ 'update:modelValue': [value: string] }>()
</script>

<template>
  <div class="segmented" role="tablist">
    <button
      v-for="option in options"
      :key="option.value"
      type="button"
      role="tab"
      class="seg-item"
      :class="{ active: option.value === modelValue }"
      :aria-selected="option.value === modelValue"
      @click="emit('update:modelValue', option.value)"
    >
      {{ option.label }}
      <span v-if="option.count !== undefined" class="seg-count">{{ option.count }}</span>
    </button>
  </div>
</template>

<style scoped>
.segmented {
  display: inline-flex;
  align-items: center;
  gap: 2px;
  height: 34px;
  padding: 3px;
  background: var(--surface);
  border: 1px solid var(--border);
  border-radius: 9px;
}

.seg-item {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  height: 26px;
  padding: 0 10px;
  border-radius: 6px;
  font-size: 12.5px;
  color: var(--text-3);
  transition: color 0.15s ease, background 0.15s ease;
  white-space: nowrap;
}

.seg-item:hover {
  color: var(--text-2);
}

.seg-item.active {
  background: var(--surface-3);
  color: var(--text);
  box-shadow: inset 0 0 0 1px var(--border-strong);
}

.seg-count {
  font-size: 11px;
  color: var(--text-3);
  font-variant-numeric: tabular-nums;
}

.seg-item.active .seg-count {
  color: var(--accent);
}
</style>
