import { onBeforeUnmount, onMounted } from 'vue'

export function usePolling(task: () => Promise<void>, interval = 3000, onError?: (error: unknown) => void) {
  let timer: number | undefined
  let inflight = false

  async function tick() {
    if (document.hidden || inflight) return
    inflight = true
    try {
      await task()
    } catch (error) {
      onError?.(error)
    } finally {
      inflight = false
    }
  }

  function handleVisibility() {
    if (!document.hidden) void tick()
  }

  onMounted(() => {
    void tick()
    timer = window.setInterval(() => void tick(), interval)
    document.addEventListener('visibilitychange', handleVisibility)
  })

  onBeforeUnmount(() => {
    if (timer !== undefined) window.clearInterval(timer)
    document.removeEventListener('visibilitychange', handleVisibility)
  })

  return { refresh: tick }
}
