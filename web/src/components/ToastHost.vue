<script setup lang="ts">
import Icon from './Icon.vue'
import { dismissToast, toasts } from '../toast'
</script>

<template>
  <Teleport to="body">
    <div class="toast-host" aria-live="polite">
      <TransitionGroup name="toast">
        <button
          v-for="item in toasts"
          :key="item.id"
          type="button"
          class="toast-item"
          :class="item.tone"
          @click="dismissToast(item.id)"
        >
          <span class="toast-icon">
            <Icon :name="item.tone === 'success' ? 'check' : item.tone === 'error' ? 'alert' : 'info'" :size="16" />
          </span>
          <span class="toast-text">{{ item.message }}</span>
        </button>
      </TransitionGroup>
    </div>
  </Teleport>
</template>

<style scoped>
.toast-host {
  position: fixed;
  left: 50%;
  bottom: 26px;
  transform: translateX(-50%);
  z-index: 200;
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 8px;
  pointer-events: none;
  width: max-content;
  max-width: min(520px, calc(100vw - 32px));
}

.toast-item {
  pointer-events: auto;
  display: flex;
  align-items: center;
  gap: 9px;
  min-height: 40px;
  padding: 9px 14px;
  background: #242424;
  border: 1px solid var(--border-strong);
  border-radius: 10px;
  box-shadow: 0 12px 30px rgba(0, 0, 0, 0.4);
  font-size: 13px;
  color: var(--text);
  text-align: left;
}

.toast-icon {
  display: inline-flex;
  color: var(--accent);
  flex-shrink: 0;
}

.toast-item.error .toast-icon {
  color: var(--danger);
}

.toast-item.info .toast-icon {
  color: var(--text-2);
}

.toast-text {
  min-width: 0;
}

.toast-enter-active,
.toast-leave-active {
  transition: opacity 0.18s ease, transform 0.18s ease;
}

.toast-enter-from,
.toast-leave-to {
  opacity: 0;
  transform: translateY(8px);
}
</style>
