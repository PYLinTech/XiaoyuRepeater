<script setup lang="ts">
import { computed, ref } from 'vue'
import { RouterLink, useRouter } from 'vue-router'
import EmptyState from '../components/EmptyState.vue'
import Icon from '../components/Icon.vue'
import { api } from '../api'
import { formatBytes, formatDuration, formatNumber, formatRelative } from '../format'
import type { OverviewResponse, RoomInfo } from '../types'
import { usePolling } from '../usePolling'

const router = useRouter()
const data = ref<OverviewResponse | null>(null)
const syncError = ref(false)

async function load() {
  try {
    data.value = await api<OverviewResponse>('/api/admin/overview')
    syncError.value = false
  } catch {
    syncError.value = true
  }
}

const { refresh } = usePolling(load, 3000)

const stats = computed(() => data.value?.stats)
const rooms = computed<RoomInfo[]>(() => data.value?.rooms.slice(0, 6) ?? [])
const agents = computed(() => data.value?.agents ?? [])
const events = computed(() => data.value?.events.slice(0, 10) ?? [])

const counters = computed(() => {
  const s = stats.value
  return [
    {
      key: 'rooms',
      icon: 'rooms',
      label: '活跃房间',
      value: formatNumber(s?.active_rooms ?? 0),
      foot: `累计创建 ${formatNumber(s?.rooms_created ?? 0)} 个`,
    },
    {
      key: 'users',
      icon: 'users',
      label: '在线用户',
      value: formatNumber(s?.participants ?? 0),
      foot: `累计加入 ${formatNumber(s?.joins_total ?? 0)} 人次`,
    },
    {
      key: 'agents',
      icon: 'server',
      label: '使用方连接',
      value: formatNumber(s?.agents_online ?? 0),
      foot: agents.value.length ? '双向通道已建立' : '等待使用方接入',
    },
    {
      key: 'traffic',
      icon: 'activity',
      label: '累计转发',
      value: formatNumber(s?.messages_relayed ?? 0),
      unit: '条',
      foot: `${formatBytes(s?.bytes_relayed ?? 0)} · 鉴权 ${formatNumber(s?.auth_requests ?? 0)} 次`,
    },
  ]
})

function eventTone(kind: string) {
  if (kind.includes('offline') || kind.includes('fail')) return 'warn'
  if (kind.includes('closed') || kind.includes('left')) return 'muted'
  return 'accent'
}

function openRoom(room: RoomInfo) {
  router.push({ path: '/rooms', query: { room: room.id } })
}

function agentInitials(name: string) {
  return name.trim().slice(0, 1).toUpperCase() || 'A'
}
</script>

<template>
  <div>
    <header class="page-head">
      <div>
        <h1 class="page-title">概览</h1>
        <p class="page-sub">
          中继服务运行状态
          <template v-if="data"> · 已运行 {{ formatDuration(data.uptime_seconds) }}</template>
        </p>
      </div>
      <div class="sync" :class="{ error: syncError }">
        <span class="sync-dot" />
        <span>{{ syncError ? '同步中断，正在重试' : '实时同步' }}</span>
      </div>
    </header>

    <div v-if="syncError" class="card error-banner">
      <span>暂时连不上服务，页面内容可能不是最新的。</span>
      <button class="btn btn-ghost btn-sm" type="button" @click="refresh">立即重试</button>
    </div>

    <div v-if="!data" class="stat-grid">
      <div v-for="n in 4" :key="n" class="stat-card">
        <div class="skeleton stat-skeleton-icon" />
        <div class="skeleton stat-skeleton-label" />
        <div class="skeleton stat-skeleton-value" />
        <div class="skeleton stat-skeleton-foot" />
      </div>
    </div>
    <div v-else class="stat-grid">
      <div v-for="item in counters" :key="item.key" class="stat-card">
        <div class="stat-icon">
          <Icon :name="item.icon" :size="18" />
        </div>
        <div class="stat-label">{{ item.label }}</div>
        <div class="stat-value">
          {{ item.value }}<span v-if="item.unit" class="stat-unit">{{ item.unit }}</span>
        </div>
        <div class="stat-foot">{{ item.foot }}</div>
      </div>
    </div>

    <div class="overview-grid">
      <section class="card panel">
        <div class="panel-head">
          <h2 class="panel-title">最近动态</h2>
          <span class="panel-hint">最新 10 条</span>
        </div>
        <div v-if="!data" class="skeleton-rows">
          <div v-for="n in 6" :key="n" class="skeleton skeleton-row" />
        </div>
        <ul v-else-if="events.length" class="event-list">
          <li v-for="event in events" :key="event.id" class="event-row">
            <span class="event-dot" :class="eventTone(event.kind)" />
            <span class="event-message" :title="event.message">{{ event.message }}</span>
            <span class="event-time" :title="event.time">{{ formatRelative(event.time) }}</span>
          </li>
        </ul>
        <EmptyState
          v-else
          title="还没有动态"
          description="房间创建、用户加入等事件会出现在这里。"
          icon="activity"
        />
      </section>

      <section class="card panel">
        <div class="panel-head">
          <h2 class="panel-title">使用方连接</h2>
          <span class="panel-hint">{{ agents.length }} 个在线</span>
        </div>
        <div v-if="!data" class="skeleton-rows">
          <div v-for="n in 3" :key="n" class="skeleton skeleton-row tall" />
        </div>
        <ul v-else-if="agents.length" class="agent-list">
          <li v-for="agent in agents.slice(0, 5)" :key="agent.agent_id" class="agent-row">
            <div class="agent-avatar">{{ agentInitials(agent.key_name) }}</div>
            <div class="agent-main">
              <div class="agent-name" :title="agent.key_name">{{ agent.key_name }}</div>
              <div class="agent-meta">
                已连接 {{ formatRelative(agent.connected_at) }} · {{ agent.rooms }} 个房间 ·
                {{ agent.participants }} 位用户
              </div>
            </div>
            <span class="dot online" title="连接正常" />
          </li>
          <li v-if="agents.length > 5" class="agent-more">还有 {{ agents.length - 5 }} 个连接</li>
        </ul>
        <EmptyState
          v-else
          title="等待使用方接入"
          description="使用方程序携带 API 密钥连接后，会显示在这里。"
          icon="server"
        />
      </section>
    </div>

    <section class="card panel room-panel">
      <div class="panel-head">
        <h2 class="panel-title">中转房间</h2>
        <RouterLink class="panel-link" to="/rooms">
          查看全部
          <Icon name="chevron-right" :size="14" />
        </RouterLink>
      </div>
      <div v-if="!data" class="skeleton-rows">
        <div v-for="n in 3" :key="n" class="skeleton skeleton-row" />
      </div>
      <div v-else-if="rooms.length" class="table-wrap">
        <table class="table">
          <thead>
            <tr>
              <th>房间</th>
              <th>使用方</th>
              <th class="num">在线用户</th>
              <th>状态</th>
              <th class="action-col" />
            </tr>
          </thead>
          <tbody>
            <tr v-for="room in rooms" :key="room.id" class="clickable" @click="openRoom(room)">
              <td>
                <div class="cell-title" :title="room.name">{{ room.name }}</div>
                <div class="cell-sub mono">{{ room.id }}</div>
              </td>
              <td><span class="cell-text">{{ room.key_name }}</span></td>
              <td class="num">{{ room.participant_count }}</td>
              <td>
                <span class="status" :class="room.agent_online ? 'ok' : 'warn'">
                  <span class="status-dot" />
                  {{ room.agent_online ? '运行中' : '使用方离线' }}
                </span>
              </td>
              <td class="action-col">
                <Icon name="chevron-right" :size="15" class="row-arrow" />
              </td>
            </tr>
          </tbody>
        </table>
      </div>
      <EmptyState
        v-else
        title="还没有中转房间"
        description="使用方程序创建房间后，会显示在这里。"
        icon="rooms"
      />
    </section>
  </div>
</template>

<style scoped>
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

.stat-grid {
  display: grid;
  grid-template-columns: repeat(4, minmax(0, 1fr));
  gap: 14px;
}

.stat-card {
  position: relative;
  padding: 17px 18px 16px;
  background: var(--surface-1);
  border: 1px solid var(--border);
  border-radius: 14px;
  overflow: hidden;
}

.stat-card::after {
  content: '';
  position: absolute;
  inset: 0 0 auto;
  height: 1px;
  background: linear-gradient(90deg, transparent, rgba(255, 255, 255, 0.08), transparent);
}

.stat-icon {
  display: flex;
  align-items: center;
  justify-content: center;
  width: 34px;
  height: 34px;
  margin-bottom: 13px;
  border-radius: 9px;
  background: #232323;
  border: 1px solid #303030;
  color: var(--text-2);
}

.stat-label {
  font-size: 12px;
  color: var(--text-3);
}

.stat-value {
  margin: 4px 0 7px;
  font-size: 27px;
  font-weight: 600;
  letter-spacing: -0.02em;
  line-height: 1.15;
  font-variant-numeric: tabular-nums;
}

.stat-unit {
  margin-left: 4px;
  font-size: 12px;
  font-weight: 400;
  color: var(--text-3);
}

.stat-foot {
  font-size: 11.5px;
  color: #6a6a6a;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.stat-skeleton-icon {
  width: 34px;
  height: 34px;
  border-radius: 9px;
  margin-bottom: 13px;
}

.stat-skeleton-label {
  width: 54px;
  height: 10px;
  margin-bottom: 9px;
}

.stat-skeleton-value {
  width: 84px;
  height: 25px;
  margin-bottom: 9px;
}

.stat-skeleton-foot {
  width: 70%;
  height: 10px;
}

.overview-grid {
  display: grid;
  grid-template-columns: minmax(0, 1.45fr) minmax(0, 1fr);
  gap: 14px;
  margin-top: 14px;
}

.panel {
  padding: 0;
  overflow: hidden;
}

.panel-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding: 15px 18px;
  border-bottom: 1px solid var(--border);
}

.panel-title {
  font-size: 13.5px;
  font-weight: 600;
}

.panel-hint {
  font-size: 11.5px;
  color: var(--text-3);
}

.panel-link {
  display: inline-flex;
  align-items: center;
  gap: 2px;
  font-size: 12px;
  color: var(--text-3);
  transition: color 0.15s ease;
}

.panel-link:hover {
  color: var(--accent);
}

.event-list {
  padding: 4px 0;
}

.event-row {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 9px 18px;
  transition: background 0.12s ease;
}

.event-row:hover {
  background: #1e1e1e;
}

.event-dot {
  flex-shrink: 0;
  width: 5px;
  height: 5px;
  border-radius: 50%;
  background: var(--text-3);
}

.event-dot.accent {
  background: var(--accent);
}

.event-dot.warn {
  background: var(--warn);
}

.event-dot.muted {
  background: #4a4a4a;
}

.event-message {
  flex: 1;
  min-width: 0;
  font-size: 12.5px;
  color: var(--text-2);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.event-time {
  flex-shrink: 0;
  font-size: 11px;
  color: var(--text-3);
  font-variant-numeric: tabular-nums;
}

.agent-list {
  padding: 6px 0;
}

.agent-row {
  display: flex;
  align-items: center;
  gap: 11px;
  padding: 10px 18px;
}

.agent-row:hover {
  background: #1e1e1e;
}

.agent-avatar {
  display: flex;
  align-items: center;
  justify-content: center;
  flex-shrink: 0;
  width: 32px;
  height: 32px;
  border-radius: 9px;
  background: #242424;
  border: 1px solid #303030;
  font-size: 13px;
  font-weight: 600;
  color: var(--text-2);
}

.agent-main {
  flex: 1;
  min-width: 0;
}

.agent-name {
  font-size: 12.5px;
  font-weight: 500;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.agent-meta {
  margin-top: 3px;
  font-size: 11px;
  color: var(--text-3);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.agent-more {
  padding: 7px 18px 9px;
  font-size: 11.5px;
  color: var(--text-3);
}

.room-panel {
  margin-top: 14px;
}

.skeleton-rows {
  padding: 12px 18px;
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.skeleton-row {
  width: 100%;
  height: 16px;
}

.skeleton-row.tall {
  height: 34px;
}

.row-arrow {
  color: var(--text-3);
  opacity: 0;
  transition: opacity 0.12s ease;
}

.clickable:hover .row-arrow {
  opacity: 1;
}

@media (max-width: 1180px) {
  .stat-grid {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
}

@media (max-width: 960px) {
  .overview-grid {
    grid-template-columns: minmax(0, 1fr);
  }
}

@media (max-width: 560px) {
  .stat-grid {
    gap: 10px;
  }

  .stat-card {
    padding: 14px;
  }

  .stat-value {
    font-size: 23px;
  }

  .error-banner {
    flex-direction: column;
    align-items: flex-start;
  }
}
</style>
