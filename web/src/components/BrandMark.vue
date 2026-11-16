<script setup lang="ts">
withDefaults(defineProps<{ size?: number }>(), { size: 36 })
</script>

<template>
  <img
    src="/favicon.svg"
    :width="size"
    :height="size"
    alt=""
    aria-hidden="true"
    draggable="false"
    class="brand-mark"
  />
</template>
