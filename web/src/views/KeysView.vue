<script setup lang="ts">
import { computed, ref } from 'vue'
import ConfirmDialog from '../components/ConfirmDialog.vue'
import CopyButton from '../components/CopyButton.vue'
import EmptyState from '../components/EmptyState.vue'
import Icon from '../components/Icon.vue'
import Modal from '../components/Modal.vue'
import { api, ApiError } from '../api'
import { formatFullTime, formatRelative, formatTime } from '../format'
import type { CreateKeyResponse, KeyItem, KeysResponse } from '../types'
import { toast } from '../toast'
import { usePolling } from '../usePolling'

const keys = ref<KeyItem[]>([])
const loaded = ref(false)
const syncError = ref(false)

const createOpen = ref(false)
const name = ref('')
const creating = ref(false)
const createError = ref('')

const createdKey = ref<CreateKeyResponse | null>(null)
const secretOpen = computed(() => createdKey.value !== null)

const deleteTarget = ref<KeyItem | null>(null)
const deleting = ref(false)

async function load() {
  try {
    const res = await api<KeysResponse>('/api/admin/keys')
    keys.value = res.keys ?? []
    loaded.value = true
    syncError.value = false
  } catch {
    syncError.value = true
  }
}

const { refresh } = usePolling(load, 4000)

const onlineCount = computed(() => keys.value.filter((key) => key.agent_online).length)

function openCreate() {
  name.value = ''
  createError.value = ''
  createOpen.value = true
}

async function createKey() {
  if (creating.value) return
  const trimmed = name.value.trim()
  if (!trimmed) {
    createError.value = '请填写密钥名称'
    return
  }
  creating.value = true
  createError.value = ''
  try {
    const res = await api<CreateKeyResponse>('/api/admin/keys', { body: { name: trimmed } })
    createOpen.value = false
    createdKey.value = res
    await refresh()
  } catch (err) {
    createError.value = err instanceof ApiError ? err.message : '创建失败，请稍后重试'
  } finally {
    creating.value = false
  }
}

async function deleteKey() {
  const target = deleteTarget.value
  if (!target || deleting.value) return
  deleting.value = true
  try {
    await api(`/api/admin/keys/${encodeURIComponent(target.id)}`, { method: 'DELETE' })
    toast('密钥已删除，关联房间已关闭', 'success')
    deleteTarget.value = null
    await refresh()
  } catch (err) {
    toast(err instanceof ApiError ? err.message : '删除失败，请稍后重试', 'error')
  } finally {
    deleting.value = false
  }
}
</script>

<template>
  <div>
    <header class="page-head">
      <div>
        <h1 class="page-title">API 密钥</h1>
        <p class="page-sub">使用方凭密钥连接中继、创建房间。共 {{ keys.length }} 个，{{ onlineCount }} 个已连接。</p>
      </div>
      <div class="head-actions">
        <div class="sync" :class="{ error: syncError }">
          <span class="sync-dot" />
          <span>{{ syncError ? '同步中断' : '实时同步' }}</span>
        </div>
        <button class="btn btn-primary" type="button" @click="openCreate">
          <Icon name="plus" :size="15" />
          创建密钥
        </button>
      </div>
    </header>

    <div v-if="syncError" class="card error-banner">
      <span>暂时连不上服务，列表可能不是最新的。</span>
      <button class="btn btn-ghost btn-sm" type="button" @click="refresh">立即重试</button>
    </div>

    <section class="card list-card">
      <div class="list-note">
        <Icon name="info" :size="15" />
        <span>完整密钥只在创建时显示一次，请妥善保存。删除密钥会同时关闭它名下的所有房间。</span>
      </div>

      <div v-if="!loaded && !syncError" class="skeleton-rows">
        <div v-for="n in 5" :key="n" class="skeleton skeleton-row" />
      </div>

      <div v-else-if="keys.length" class="table-wrap">
        <table class="table keys-table">
          <thead>
            <tr>
              <th>名称</th>
              <th>密钥前缀</th>
              <th>连接状态</th>
              <th class="num">房间</th>
              <th class="num">在线用户</th>
              <th>最近使用</th>
              <th>创建时间</th>
              <th class="action-col" />
            </tr>
          </thead>
          <tbody>
            <tr v-for="key in keys" :key="key.id">
              <td>
                <div class="cell-title" :title="key.name">
                  <span class="cell-main">{{ key.name }}</span>
                </div>
                <div class="cell-sub">{{ key.id }}</div>
              </td>
              <td>
                <span class="key-prefix mono" :title="key.prefix">{{ key.prefix }}…</span>
              </td>
              <td>
                <span class="status" :class="key.agent_online ? 'ok' : 'idle'">
                  <span class="status-dot" />
                  {{ key.agent_online ? '使用方已连接' : '未连接' }}
                </span>
              </td>
              <td class="num">{{ key.rooms }}</td>
              <td class="num">{{ key.participants }}</td>
              <td class="cell-muted nowrap" :title="key.last_used_at ? formatFullTime(key.last_used_at) : ''">
                {{ key.last_used_at ? formatRelative(key.last_used_at) : '从未使用' }}
              </td>
              <td class="cell-muted nowrap" :title="formatFullTime(key.created_at)">{{ formatTime(key.created_at) }}</td>
              <td class="action-col">
                <button
                  class="icon-btn danger"
                  type="button"
                  title="删除密钥"
                  aria-label="删除密钥"
                  @click="deleteTarget = key"
                >
                  <Icon name="trash" :size="15" />
                </button>
              </td>
            </tr>
          </tbody>
        </table>
      </div>

      <EmptyState
        v-else
        title="还没有 API 密钥"
        description="创建密钥后交给使用方程序，就能开始建立中转房间。"
        icon="keys"
      >
        <button class="btn btn-primary btn-sm" type="button" @click="openCreate">
          <Icon name="plus" :size="14" />
          创建第一个密钥
        </button>
      </EmptyState>
    </section>

    <Modal :open="createOpen" title="创建 API 密钥" width="sm" :closable="!creating" @close="createOpen = false">
      <form class="create-form" @submit.prevent="createKey">
        <div class="field">
          <label class="field-label" for="key-name">名称</label>
          <input
            id="key-name"
            v-model="name"
            class="input"
            type="text"
            maxlength="40"
            placeholder="例如：直播分发、远程协助"
            autocomplete="off"
          />
          <p class="field-hint">用于区分不同的使用方程序，之后可以随时删除。</p>
        </div>
        <div v-if="createError" class="error-text">{{ createError }}</div>
      </form>
      <template #footer>
        <button class="btn btn-ghost" type="button" :disabled="creating" @click="createOpen = false">取消</button>
        <button class="btn btn-primary" type="button" :disabled="creating" @click="createKey">
          <span v-if="creating" class="spinner" />
          创建
        </button>
      </template>
    </Modal>

    <Modal :open="secretOpen" title="密钥已创建" width="sm" @close="createdKey = null">
      <template v-if="createdKey">
        <div class="secret-alert">
          <Icon name="alert" :size="16" />
          <span>完整密钥只显示这一次。请现在复制并保存，关闭后无法再次查看。</span>
        </div>
        <div class="secret-meta">
          <span class="secret-name" :title="createdKey.key.name">{{ createdKey.key.name }}</span>
          <span class="cell-sub">{{ createdKey.key.id }}</span>
        </div>
        <div class="secret-box">
          <code class="selectable">{{ createdKey.secret }}</code>
        </div>
        <div class="secret-actions">
          <CopyButton :value="createdKey.secret" label="复制完整密钥" />
          <span class="hint">使用方携带这个密钥连接中继服务。</span>
        </div>
      </template>
      <template #footer>
        <button class="btn btn-primary" type="button" @click="createdKey = null">我已保存</button>
      </template>
    </Modal>

    <ConfirmDialog
      :open="deleteTarget !== null"
      title="删除这个密钥？"
      :message="`删除「${deleteTarget?.name ?? ''}」后，使用方将无法再用它连接，名下的房间会被关闭，在线用户会被断开。这个操作无法撤销。`"
      confirm-text="删除密钥"
      danger
      :busy="deleting"
      @confirm="deleteKey"
      @close="deleteTarget = null"
    />
  </div>
</template>

<style scoped>
.head-actions {
  display: flex;
  align-items: center;
  gap: 16px;
}

.sync {
  display: inline-flex;
  align-items: center;
  gap: 7px;
  font-size: 12px;
  color: var(--text-3);
  white-space: nowrap;
}

.sync-dot {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: var(--accent);
  box-shadow: 0 0 0 3px var(--accent-soft);
}

.sync.error {
  color: var(--danger);
}

.sync.error .sync-dot {
  background: var(--danger);
  box-shadow: 0 0 0 3px var(--danger-soft);
}

.error-banner {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding: 11px 16px;
  margin-bottom: 14px;
  font-size: 12.5px;
  color: var(--danger);
  border-color: var(--danger-line);
  background: var(--danger-soft);
}

.list-card {
  overflow: hidden;
}

.list-note {
  display: flex;
  align-items: flex-start;
  gap: 9px;
  padding: 12px 18px;
  border-bottom: 1px solid var(--border);
  background: #191919;
  font-size: 12px;
  line-height: 1.6;
  color: var(--text-3);
}

.list-note :deep(.ri-icon) {
  flex-shrink: 0;
  margin-top: 1px;
  color: var(--text-3);
}

.skeleton-rows {
  padding: 16px 18px;
  display: flex;
  flex-direction: column;
  gap: 14px;
}

.skeleton-row {
  width: 100%;
  height: 16px;
}

.keys-table {
  min-width: 880px;
}

.key-prefix {
  display: inline-block;
  max-width: 150px;
  padding: 2px 7px;
  border-radius: 5px;
  background: #222;
  border: 1px solid var(--border);
  font-size: 11.5px;
  color: var(--text-2);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  vertical-align: middle;
}

.status.idle {
  color: var(--text-3);
}

.icon-btn.danger:hover {
  color: var(--danger);
  background: var(--danger-soft);
}

.nowrap {
  white-space: nowrap;
}

.field-hint {
  margin-top: 6px;
  font-size: 11.5px;
  color: var(--text-3);
}

.create-form {
  padding-bottom: 6px;
}

.secret-alert {
  display: flex;
  align-items: flex-start;
  gap: 9px;
  padding: 11px 12px;
  border-radius: 9px;
  background: var(--warn-soft);
  border: 1px solid rgba(226, 179, 94, 0.28);
  color: var(--warn);
  font-size: 12.5px;
  line-height: 1.6;
}

.secret-alert :deep(.ri-icon) {
  flex-shrink: 0;
  margin-top: 1px;
}

.secret-meta {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  margin-top: 18px;
}

.secret-name {
  min-width: 0;
  font-size: 13.5px;
  font-weight: 600;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.secret-box {
  margin-top: 9px;
  padding: 13px 14px;
  border-radius: 9px;
  background: #141414;
  border: 1px solid var(--border-strong);
}

.secret-box code {
  display: block;
  font-family: var(--mono);
  font-size: 12.5px;
  line-height: 1.7;
  color: var(--accent);
  word-break: break-all;
}

.secret-actions {
  display: flex;
  align-items: center;
  gap: 12px;
  margin-top: 12px;
  padding-bottom: 8px;
}

@media (max-width: 760px) {
  .head-actions {
    gap: 10px;
  }

  .sync {
    display: none;
  }

  .error-banner {
    flex-direction: column;
    align-items: flex-start;
  }

  .secret-actions {
    flex-direction: column;
    align-items: flex-start;
    gap: 8px;
  }
}
</style>
