<script setup lang="ts">
import Modal from './Modal.vue'

withDefaults(
  defineProps<{
    open: boolean
    title: string
    message: string
    confirmText?: string
    cancelText?: string
    danger?: boolean
    busy?: boolean
  }>(),
  { confirmText: '确认', cancelText: '取消', danger: false, busy: false },
)

const emit = defineEmits<{ confirm: []; close: [] }>()
</script>

<template>
  <Modal :open="open" :title="title" width="sm" :closable="!busy" @close="emit('close')">
    <p class="confirm-message">{{ message }}</p>
    <template #footer>
      <button class="btn btn-ghost" type="button" :disabled="busy" @click="emit('close')">
        {{ cancelText }}
      </button>
      <button
        class="btn"
        :class="danger ? 'btn-danger' : 'btn-primary'"
        type="button"
        :disabled="busy"
        @click="emit('confirm')"
      >
        <span v-if="busy" class="spinner" :class="{ light: !danger }" />
        {{ confirmText }}
      </button>
    </template>
  </Modal>
</template>

<style scoped>
.confirm-message {
  margin: 2px 0 12px;
  font-size: 13.5px;
  line-height: 1.75;
  color: var(--text-2);
}
</style>
