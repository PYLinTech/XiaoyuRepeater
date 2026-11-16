import { reactive } from 'vue'

export type ToastTone = 'success' | 'error' | 'info'

export interface ToastItem {
  id: number
  message: string
  tone: ToastTone
}

export const toasts = reactive<ToastItem[]>([])

let nextId = 1

export function toast(message: string, tone: ToastTone = 'success') {
  const item: ToastItem = { id: nextId++, message, tone }
  toasts.push(item)
  if (toasts.length > 4) {
    toasts.splice(0, toasts.length - 4)
  }
  window.setTimeout(() => dismissToast(item.id), 3200)
}

export function dismissToast(id: number) {
  const index = toasts.findIndex((item) => item.id === id)
  if (index >= 0) toasts.splice(index, 1)
}
