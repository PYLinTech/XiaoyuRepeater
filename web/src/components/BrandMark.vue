<script setup lang="ts">
withDefaults(defineProps<{ size?: number }>(), { size: 36 })
</script>

<template>
  <svg :width="size" :height="size" viewBox="0 0 32 32" fill="none" aria-hidden="true" class="brand-mark">
    <rect width="32" height="32" rx="9" fill="#1c1c1c" />
    <rect x="0.75" y="0.75" width="30.5" height="30.5" rx="8.25" stroke="#333" />
    <path
      d="M5 16c4-5.4 9-8.1 14-6.8 2.7.8 5 2.8 6.6 5.5-1.6 2.7-3.9 4.7-6.6 5.5-5 1.3-10-1.4-14-6.8Z"
      stroke="#49d9a7"
      stroke-width="1.7"
      stroke-linejoin="round"
    />
    <circle cx="10" cy="16" r="1.9" fill="#49d9a7" />
    <path
      d="M25.5 8.5a8 8 0 0 1 0 15"
      stroke="#49d9a7"
      stroke-width="1.4"
      stroke-linecap="round"
      opacity=".5"
    />
  </svg>
</template>
