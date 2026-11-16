<script setup lang="ts">
withDefaults(
  defineProps<{
    name: string
    size?: number
  }>(),
  { size: 18 },
)
</script>

<template>
  <svg
    :width="size"
    :height="size"
    viewBox="0 0 24 24"
    fill="none"
    stroke="currentColor"
    stroke-width="1.7"
    stroke-linecap="round"
    stroke-linejoin="round"
    aria-hidden="true"
  >
    <template v-if="name === 'overview'">
      <rect x="3.5" y="3.5" width="7" height="7" rx="1.7" />
      <rect x="13.5" y="3.5" width="7" height="7" rx="1.7" />
      <rect x="3.5" y="13.5" width="7" height="7" rx="1.7" />
      <rect x="13.5" y="13.5" width="7" height="7" rx="1.7" />
    </template>
    <template v-else-if="name === 'rooms'">
      <path d="M2.5 9.3a14 14 0 0 1 19 0" />
      <path d="M5.6 12.8a9.5 9.5 0 0 1 12.8 0" />
      <path d="M8.8 16.2a5 5 0 0 1 6.4 0" />
      <circle cx="12" cy="19.4" r="1.2" fill="currentColor" stroke="none" />
    </template>
    <template v-else-if="name === 'keys'">
      <circle cx="8.2" cy="15.2" r="4.2" />
      <path d="M11.2 12.2 19.5 3.9M16.2 7.2l3 3M13.6 9.8l3 3" />
    </template>
    <template v-else-if="name === 'copy'">
      <rect x="8.5" y="8.5" width="11" height="11" rx="2.2" />
      <path d="M15.5 8.5v-2a2.2 2.2 0 0 0-2.2-2.2H5.7a2.2 2.2 0 0 0-2.2 2.2v7.6a2.2 2.2 0 0 0 2.2 2.2h2" />
    </template>
    <template v-else-if="name === 'plus'">
      <path d="M12 5v14M5 12h14" />
    </template>
    <template v-else-if="name === 'close'">
      <path d="M6 6l12 12M18 6 6 18" />
    </template>
    <template v-else-if="name === 'search'">
      <circle cx="10.5" cy="10.5" r="6.5" />
      <path d="m15.5 15.5 4.5 4.5" />
    </template>
    <template v-else-if="name === 'chevron-right'">
      <path d="m9 5 7 7-7 7" />
    </template>
    <template v-else-if="name === 'logout'">
      <path d="M14.5 4h3.2A2.3 2.3 0 0 1 20 6.3v11.4a2.3 2.3 0 0 1-2.3 2.3h-3.2" />
      <path d="m10 8-4 4 4 4M6 12h9" />
    </template>
    <template v-else-if="name === 'lock'">
      <rect x="4.5" y="10" width="15" height="10.5" rx="2.4" />
      <path d="M8 10V7.5a4 4 0 0 1 8 0V10" />
    </template>
    <template v-else-if="name === 'eye'">
      <path d="M2.5 12S6 5.8 12 5.8 21.5 12 21.5 12 18 18.2 12 18.2 2.5 12 2.5 12Z" />
      <circle cx="12" cy="12" r="2.8" />
    </template>
    <template v-else-if="name === 'eye-off'">
      <path d="M4 4l16 16" />
      <path d="M9.9 6.2A10.9 10.9 0 0 1 12 6c6 0 9.5 6 9.5 6a17 17 0 0 1-3 3.5M6.2 8.3A16.6 16.6 0 0 0 2.5 12S6 18 12 18a10 10 0 0 0 3.6-.6" />
      <path d="M10 10a2.8 2.8 0 0 0 4 4" />
    </template>
    <template v-else-if="name === 'activity'">
      <path d="M3 12h4l2.5-6.5L14 19l2.5-7H21" />
    </template>
    <template v-else-if="name === 'users'">
      <circle cx="9" cy="8.5" r="3.5" />
      <path d="M3 20a6 6 0 0 1 12 0" />
      <path d="M16 5.6a3.5 3.5 0 0 1 0 6.7M18.6 20a6 6 0 0 0-3-5.2" />
    </template>
    <template v-else-if="name === 'message'">
      <path d="M20 11.5a7.5 7.5 0 0 1-7.5 7.5H8l-4 3v-5.4A7.5 7.5 0 0 1 11.5 4h1A7.5 7.5 0 0 1 20 11.5Z" />
    </template>
    <template v-else-if="name === 'server'">
      <rect x="3.5" y="4" width="17" height="7" rx="2" />
      <rect x="3.5" y="13" width="17" height="7" rx="2" />
      <circle cx="7.2" cy="7.5" r=".8" fill="currentColor" stroke="none" />
      <circle cx="7.2" cy="16.5" r=".8" fill="currentColor" stroke="none" />
      <path d="M11 7.5h6M11 16.5h6" />
    </template>
    <template v-else-if="name === 'check'">
      <path d="m5 12.5 4.5 4.5L19 7" />
    </template>
    <template v-else-if="name === 'alert'">
      <path d="M12 3.5 21 20H3L12 3.5Z" />
      <path d="M12 9v5M12 17.2v.1" />
    </template>
    <template v-else-if="name === 'info'">
      <circle cx="12" cy="12" r="8.5" />
      <path d="M12 11v5M12 8v.1" />
    </template>
    <template v-else-if="name === 'refresh'">
      <path d="M20 5v5h-5" />
      <path d="M20 10a8 8 0 1 0 .6 4" />
    </template>
    <template v-else-if="name === 'trash'">
      <path d="M4 7h16M9.5 7V4.8a1.3 1.3 0 0 1 1.3-1.3h2.4a1.3 1.3 0 0 1 1.3 1.3V7" />
      <path d="M6.4 7l.8 12a2 2 0 0 0 2 1.9h5.6a2 2 0 0 0 2-1.9l.8-12" />
      <path d="M10 11v6M14 11v6" />
    </template>
    <template v-else>
      <circle cx="12" cy="12" r="8.5" />
    </template>
  </svg>
</template>
