<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import ConfirmDialog from '../components/ConfirmDialog.vue'
import CopyButton from '../components/CopyButton.vue'
import Icon from '../components/Icon.vue'
import { api, ApiError } from '../api'
import { formatBytes, formatFullTime, formatNumber, formatRelative } from '../format'
import type { RoomInfo, RoomResponse } from '../types'
import { toast } from '../toast'
import { usePolling } from '../usePolling'

const props = defineProps<{ roomId: string }>()
const emit = defineEmits<{ close: []; closed: [] }>()

const room = ref<RoomInfo | null>(null)
const syncError = ref(false)
const failed = ref(false)
const confirmOpen = ref(false)
const closing = ref(false)

async function load() {
  try {
    const res = await api<RoomResponse>(`/api/admin/rooms/${encodeURIComponent(props.roomId)}`)
    room.value = res.room
    syncError.value = false
    failed.value = false
  } catch (err) {
    if (err instanceof ApiError && err.status === 404) {
      toast('房间已关闭', 'info')
      emit('closed')
      emit('close')
      return
    }
    syncError.value = true
    if (!room.value) failed.value = true
  }
}

usePolling(load, 2500)

async function closeRoom() {
  if (closing.value) return
  closing.value = true
  try {
    await api(`/api/admin/rooms/${encodeURIComponent(props.roomId)}`, { method: 'DELETE' })
    toast('房间已关闭，在线用户已断开', 'success')
    confirmOpen.value = false
    emit('closed')
    emit('close')
  } catch (err) {
    toast(err instanceof ApiError ? err.message : '关闭失败，请稍后重试', 'error')
  } finally {
    closing.value = false
  }
}

function onKeydown(event: KeyboardEvent) {
  if (event.key === 'Escape' && !confirmOpen.value) emit('close')
}

onMounted(() => {
  document.body.style.overflow = 'hidden'
  document.addEventListener('keydown', onKeydown)
})

onBeforeUnmount(() => {
  document.body.style.overflow = ''
  document.removeEventListener('keydown', onKeydown)
})

function avatarText(subject: string) {
  return subject.trim().slice(0, 1).toUpperCase() || '用'
}
</script>

<template>
  <Teleport to="body">
    <Transition name="drawer">
      <div class="drawer-overlay" @mousedown.self="emit('close')">
        <aside class="drawer-panel" role="dialog" aria-modal="true" aria-label="房间详情">
          <header class="drawer-head">
            <div class="drawer-head-main">
              <h2 class="drawer-title">房间详情</h2>
              <span v-if="syncError" class="drawer-sync">同步中断，正在重试</span>
            </div>
            <button class="icon-btn" type="button" aria-label="关闭" @click="emit('close')">
              <Icon name="close" :size="18" />
            </button>
          </header>

          <div class="drawer-body">
            <div v-if="!room && !failed" class="drawer-loading">
              <div class="skeleton" style="height: 108px; border-radius: 12px" />
              <div class="skeleton" style="height: 150px; border-radius: 12px" />
              <div class="skeleton" style="height: 150px; border-radius: 12px" />
            </div>

            <div v-else-if="failed" class="drawer-failed">
              <p>暂时读不到这个房间的信息。</p>
              <button class="btn btn-ghost btn-sm" type="button" @click="load">重新加载</button>
            </div>

            <template v-else-if="room">
              <section class="room-hero">
                <div class="hero-status">
                  <span class="status" :class="room.agent_online ? 'ok' : 'warn'">
                    <span class="status-dot" />
                    {{ room.agent_online ? '运行中' : '使用方离线' }}
                  </span>
                  <span class="hero-capacity">{{ room.participant_count }} / {{ room.max_participants }} 人</span>
                </div>
                <h3 class="hero-name" :title="room.name">{{ room.name }}</h3>
                <div class="hero-id">
                  <span class="mono" :title="room.id">{{ room.id }}</span>
                  <CopyButton :value="room.id" icon-only label="复制房间 ID" />
                </div>
              </section>

              <section class="drawer-section">
                <h4 class="section-title">房间信息</h4>
                <dl class="info-grid">
                  <div class="info-row">
                    <dt>所属使用方</dt>
                    <dd :title="room.key_name">{{ room.key_name }}</dd>
                  </div>
                  <div class="info-row">
                    <dt>创建时间</dt>
                    <dd :title="formatFullTime(room.created_at)">{{ formatRelative(room.created_at) }}</dd>
                  </div>
                  <div class="info-row">
                    <dt>累计消息</dt>
                    <dd>{{ formatNumber(room.messages) }} 条</dd>
                  </div>
                  <div class="info-row">
                    <dt>累计流量</dt>
                    <dd>{{ formatBytes(room.bytes) }}</dd>
                  </div>
                  <div class="info-row">
                    <dt>实时媒体</dt>
                    <dd>{{ room.rtc_peers }} 个连接 · {{ room.tracks }} 条轨道</dd>
                  </div>
                  <div class="info-row">
                    <dt>媒体流量</dt>
                    <dd>{{ formatNumber(room.rtp_packets) }} 包 · {{ formatBytes(room.rtp_bytes) }}</dd>
                  </div>
                </dl>
              </section>

              <section class="drawer-section">
                <h4 class="section-title">在线用户 <span class="section-count">{{ room.participants.length }}</span></h4>
                <ul v-if="room.participants.length" class="people-list">
                  <li v-for="person in room.participants" :key="person.id" class="person-row">
                    <div class="person-avatar">{{ avatarText(person.subject) }}</div>
                    <div class="person-main">
                      <div class="person-line">
                        <span class="person-name" :title="person.subject">{{ person.subject }}</span>
                        <span
                          class="transport-badge"
                          :class="person.transport"
                          :title="person.transport === 'webrtc' ? 'WebRTC 实时媒体' : 'WebSocket 消息'"
                        >
                          {{ person.transport === 'webrtc' ? 'WebRTC' : 'WS' }}
                        </span>
                      </div>
                      <div class="person-meta" :title="person.remote_addr + ' · ' + person.user_agent">
                        <template v-if="person.transport === 'webrtc'">
                          {{ person.tracks ?? 0 }} 条轨道 ·
                        </template>
                        加入于 {{ formatRelative(person.joined_at) }} · {{ person.remote_addr || '未知地址' }}
                      </div>
                    </div>
                  </li>
                </ul>
                <p v-else class="people-empty">目前没有在线用户。</p>
              </section>
            </template>
          </div>

          <footer v-if="room" class="drawer-foot">
            <button class="btn btn-ghost" type="button" @click="emit('close')">收起</button>
            <button class="btn btn-danger" type="button" @click="confirmOpen = true">
              <Icon name="close" :size="14" />
              关闭房间
            </button>
          </footer>
        </aside>
      </div>
    </Transition>

    <ConfirmDialog
      :open="confirmOpen"
      title="关闭这个房间？"
      message="房间关闭后，在线用户会被断开连接，且无法再加入。这个操作无法撤销。"
      confirm-text="关闭房间"
      danger
      :busy="closing"
      @confirm="closeRoom"
      @close="confirmOpen = false"
    />
  </Teleport>
</template>

<style scoped>
.drawer-overlay {
  position: fixed;
  inset: 0;
  z-index: 80;
  display: flex;
  justify-content: flex-end;
  background: rgba(0, 0, 0, 0.48);
  backdrop-filter: blur(2px);
  -webkit-backdrop-filter: blur(2px);
}

.drawer-panel {
  display: flex;
  flex-direction: column;
  width: min(520px, 100%);
  height: 100%;
  background: #191919;
  border-left: 1px solid var(--border-strong);
  box-shadow: -24px 0 60px rgba(0, 0, 0, 0.4);
}

.drawer-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 14px;
  flex-shrink: 0;
  height: 62px;
  padding: 0 18px 0 22px;
  border-bottom: 1px solid var(--border);
}

.drawer-head-main {
  display: flex;
  align-items: center;
  gap: 10px;
  min-width: 0;
}

.drawer-title {
  font-size: 15px;
  font-weight: 600;
}

.drawer-sync {
  font-size: 11px;
  color: var(--danger);
  white-space: nowrap;
}

.drawer-body {
  flex: 1;
  overflow-y: auto;
  padding: 18px 22px 26px;
}

.drawer-loading {
  display: flex;
  flex-direction: column;
  gap: 14px;
}

.drawer-failed {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 12px;
  padding: 60px 0;
  font-size: 13px;
  color: var(--text-2);
}

.room-hero {
  padding: 16px;
  background: #1f1f1f;
  border: 1px solid var(--border);
  border-radius: 12px;
}

.hero-status {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 10px;
}

.hero-capacity {
  font-size: 11.5px;
  color: var(--text-3);
  font-variant-numeric: tabular-nums;
}

.hero-name {
  margin-top: 12px;
  font-size: 19px;
  font-weight: 600;
  letter-spacing: -0.01em;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.hero-id {
  display: flex;
  align-items: center;
  gap: 4px;
  margin-top: 7px;
}

.hero-id .mono {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-size: 11.5px;
  color: var(--text-3);
}

.drawer-section {
  margin-top: 24px;
}

.section-title {
  margin-bottom: 10px;
  font-size: 11.5px;
  font-weight: 500;
  letter-spacing: 0.06em;
  color: var(--text-3);
}

.section-count {
  margin-left: 4px;
  padding: 1px 6px;
  border-radius: 20px;
  background: #262626;
  font-size: 10.5px;
  color: var(--text-2);
}

.info-grid {
  border: 1px solid var(--border);
  border-radius: 10px;
  overflow: hidden;
}

.info-row {
  display: grid;
  grid-template-columns: 96px minmax(0, 1fr);
  gap: 12px;
  padding: 10px 14px;
  background: #1c1c1c;
}

.info-row + .info-row {
  border-top: 1px solid var(--border);
}

.info-row dt {
  align-self: center;
  font-size: 12px;
  color: var(--text-3);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.info-row dd {
  min-width: 0;
  font-size: 12.5px;
  color: var(--text-2);
  text-align: right;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.info-row dd.mono {
  font-family: var(--mono);
  font-size: 11.5px;
}

.people-list {
  display: flex;
  flex-direction: column;
  margin: 0;
  padding: 0;
  border: 1px solid var(--border);
  border-radius: 10px;
  overflow: hidden;
  list-style: none;
}

.person-row {
  display: flex;
  align-items: center;
  gap: 11px;
  padding: 11px 14px;
  background: #1c1c1c;
}

.person-row + .person-row {
  border-top: 1px solid var(--border);
}

.person-avatar {
  display: flex;
  align-items: center;
  justify-content: center;
  flex-shrink: 0;
  width: 32px;
  height: 32px;
  border-radius: 50%;
  background: #262626;
  border: 1px solid #343434;
  font-size: 13px;
  font-weight: 600;
  color: var(--text-2);
}

.person-main {
  flex: 1;
  min-width: 0;
}

.person-line {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
}

.person-name {
  flex: 0 1 auto;
  min-width: 0;
  font-size: 12.5px;
  font-weight: 500;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.transport-badge {
  flex-shrink: 0;
  padding: 1px 6px;
  border-radius: 5px;
  border: 1px solid var(--border-strong);
  background: #242424;
  font-size: 9.5px;
  font-weight: 500;
  letter-spacing: 0.03em;
  line-height: 1.45;
  color: var(--text-3);
}

.transport-badge.webrtc {
  border-color: rgba(73, 217, 167, 0.28);
  background: var(--accent-soft);
  color: var(--accent);
}

.person-meta {
  margin-top: 3px;
  font-size: 11px;
  color: var(--text-3);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.people-empty {
  padding: 20px 14px;
  border: 1px dashed var(--border-strong);
  border-radius: 10px;
  text-align: center;
  font-size: 12px;
  color: var(--text-3);
}

.drawer-foot {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 10px;
  flex-shrink: 0;
  padding: 14px 22px;
  border-top: 1px solid var(--border);
  background: #191919;
}

.drawer-enter-active,
.drawer-leave-active {
  transition: opacity 0.2s ease;
}

.drawer-enter-active .drawer-panel,
.drawer-leave-active .drawer-panel {
  transition: transform 0.24s cubic-bezier(0.22, 0.61, 0.36, 1);
}

.drawer-enter-from,
.drawer-leave-to {
  opacity: 0;
}

.drawer-enter-from .drawer-panel,
.drawer-leave-to .drawer-panel {
  transform: translateX(36px);
}

@media (max-width: 560px) {
  .drawer-body {
    padding: 16px 16px 22px;
  }

  .drawer-head {
    padding: 0 14px 0 16px;
  }

  .drawer-foot {
    padding: 12px 16px;
  }
}
</style>
