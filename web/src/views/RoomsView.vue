<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import EmptyState from '../components/EmptyState.vue'
import Icon from '../components/Icon.vue'
import SegmentedControl from '../components/SegmentedControl.vue'
import { api } from '../api'
import { formatNumber, formatTime } from '../format'
import type { RoomInfo, RoomsResponse } from '../types'
import { usePolling } from '../usePolling'
import RoomDrawer from './RoomDrawer.vue'

const route = useRoute()
const router = useRouter()

const rooms = ref<RoomInfo[]>([])
const loaded = ref(false)
const syncError = ref(false)
const search = ref('')
const filter = ref('all')
const selectedId = ref<string | null>(typeof route.query.room === 'string' ? route.query.room : null)

async function load() {
  try {
    const res = await api<RoomsResponse>('/api/admin/rooms')
    rooms.value = res.rooms ?? []
    loaded.value = true
    syncError.value = false
  } catch {
    syncError.value = true
  }
}

const { refresh } = usePolling(load, 3000)

watch(
  () => route.query.room,
  (value) => {
    selectedId.value = typeof value === 'string' ? value : null
  },
)

const onlineCount = computed(() => rooms.value.filter((room) => room.agent_online).length)
const offlineCount = computed(() => rooms.value.length - onlineCount.value)

const filterOptions = computed(() => [
  { value: 'all', label: '全部', count: rooms.value.length },
  { value: 'online', label: '运行中', count: onlineCount.value },
  { value: 'offline', label: '使用方离线', count: offlineCount.value },
])

const filteredRooms = computed(() => {
  const needle = search.value.trim().toLowerCase()
  return rooms.value.filter((room) => {
    if (filter.value === 'online' && !room.agent_online) return false
    if (filter.value === 'offline' && room.agent_online) return false
    if (!needle) return true
    return (
      room.name.toLowerCase().includes(needle) ||
      room.id.toLowerCase().includes(needle) ||
      room.key_name.toLowerCase().includes(needle)
    )
  })
})

function hasFilter() {
  return Boolean(search.value.trim()) || filter.value !== 'all'
}

function clearFilter() {
  search.value = ''
  filter.value = 'all'
}

function openRoom(room: RoomInfo) {
  selectedId.value = room.id
  router.replace({ query: { ...route.query, room: room.id } })
}

function closeDrawer() {
  selectedId.value = null
  const query = { ...route.query }
  delete query.room
  router.replace({ query })
}
</script>

<template>
  <div>
    <header class="page-head">
      <div>
        <h1 class="page-title">中转房间</h1>
        <p class="page-sub">使用方创建的房间与在线用户，点击任意一行查看详情。</p>
      </div>
      <div class="sync" :class="{ error: syncError }">
        <span class="sync-dot" />
        <span>{{ syncError ? '同步中断，正在重试' : '实时同步' }}</span>
      </div>
    </header>

    <div v-if="syncError" class="card error-banner">
      <span>暂时连不上服务，列表可能不是最新的。</span>
      <button class="btn btn-ghost btn-sm" type="button" @click="refresh">立即重试</button>
    </div>

    <section class="card list-card">
      <div class="toolbar">
        <div class="search-box">
          <Icon name="search" :size="15" class="search-icon" />
          <input
            v-model="search"
            class="input search-input"
            type="text"
            placeholder="搜索房间名称、ID 或使用方"
            aria-label="搜索房间"
          />
          <button
            v-if="search"
            class="search-clear"
            type="button"
            aria-label="清除搜索"
            @click="search = ''"
          >
            <Icon name="close" :size="13" />
          </button>
        </div>
        <SegmentedControl v-model="filter" :options="filterOptions" />
      </div>

      <div v-if="!loaded && !syncError" class="skeleton-rows">
        <div v-for="n in 6" :key="n" class="skeleton skeleton-row" />
      </div>

      <div v-else-if="filteredRooms.length" class="table-wrap">
        <table class="table rooms-table">
          <thead>
            <tr>
              <th>房间</th>
              <th>使用方</th>
              <th class="num">在线用户</th>
              <th class="num">WS 消息</th>
              <th>创建时间</th>
              <th>状态</th>
              <th class="action-col" />
            </tr>
          </thead>
          <tbody>
            <tr v-for="room in filteredRooms" :key="room.id" class="clickable" @click="openRoom(room)">
              <td>
                <div class="cell-title" :title="room.name">{{ room.name }}</div>
                <div class="cell-sub mono">{{ room.id }}</div>
              </td>
              <td><span class="cell-text" :title="room.key_name">{{ room.key_name }}</span></td>
              <td class="num">
                {{ room.participant_count }}<span class="cell-muted"> / {{ room.max_participants }}</span>
                <div v-if="room.rtc_peers" class="cell-sub">
                  WebRTC {{ room.rtc_peers }} · 轨道 {{ room.tracks }}
                </div>
              </td>
              <td class="num">
                {{ formatNumber(room.messages) }}
                <div v-if="room.rtp_packets" class="cell-sub">
                  RTP {{ formatNumber(room.rtp_packets) }} 包
                </div>
              </td>
              <td class="cell-muted nowrap">{{ formatTime(room.created_at) }}</td>
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
        v-else-if="rooms.length"
        title="没有符合条件的房间"
        description="换个关键词，或者清除当前的筛选条件。"
        icon="search"
      >
        <button class="btn btn-ghost btn-sm" type="button" @click="clearFilter">清除筛选</button>
      </EmptyState>

      <EmptyState
        v-else
        title="还没有中转房间"
        description="使用方程序携带 API 密钥创建房间后，会显示在这里。"
        icon="rooms"
      />
    </section>

    <RoomDrawer
      v-if="selectedId"
      :key="selectedId"
      :room-id="selectedId"
      @close="closeDrawer"
      @closed="refresh"
    />
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

.list-card {
  overflow: hidden;
}

.toolbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 14px;
  padding: 14px 16px;
  border-bottom: 1px solid var(--border);
}

.search-box {
  position: relative;
  flex: 1;
  max-width: 340px;
}

.search-icon {
  position: absolute;
  left: 11px;
  top: 50%;
  transform: translateY(-50%);
  color: var(--text-3);
  pointer-events: none;
}

.search-input {
  width: 100%;
  height: 34px;
  padding-left: 33px;
  padding-right: 30px;
  font-size: 12.5px;
}

.search-clear {
  position: absolute;
  right: 5px;
  top: 50%;
  transform: translateY(-50%);
  display: flex;
  align-items: center;
  justify-content: center;
  width: 22px;
  height: 22px;
  border-radius: 5px;
  color: var(--text-3);
}

.search-clear:hover {
  background: var(--surface-3);
  color: var(--text-2);
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

.rooms-table {
  min-width: 760px;
}

.cell-sub {
  font-family: inherit;
}

.nowrap {
  white-space: nowrap;
}

.row-arrow {
  color: var(--text-3);
  opacity: 0;
  transition: opacity 0.12s ease;
}

.clickable:hover .row-arrow {
  opacity: 1;
}

@media (max-width: 760px) {
  .toolbar {
    flex-direction: column;
    align-items: stretch;
  }

  .search-box {
    max-width: none;
  }

  .error-banner {
    flex-direction: column;
    align-items: flex-start;
  }
}
</style>
