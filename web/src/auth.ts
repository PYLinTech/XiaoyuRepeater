import { reactive } from 'vue'
import { api } from './api'
import type { SessionInfo } from './types'

export const authState = reactive({
  loaded: false,
  authenticated: false,
  setupRequired: false,
  envManaged: false,
  version: '',
})

let pending: Promise<void> | null = null

async function fetchSession() {
  try {
    const data = await api<SessionInfo>('/api/admin/session')
    authState.authenticated = data.authenticated
    authState.setupRequired = data.setup_required
    authState.envManaged = data.env_managed
    authState.version = data.version
  } finally {
    authState.loaded = true
  }
}

export function ensureSession(force = false): Promise<void> {
  if (authState.loaded && !force) return Promise.resolve()
  if (!pending) {
    pending = fetchSession().finally(() => {
      pending = null
    })
  }
  return pending
}

export async function refreshSession() {
  await fetchSession()
}

export function markLoggedOut() {
  authState.authenticated = false
}
