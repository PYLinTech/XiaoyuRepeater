export interface StatsSnapshot {
  agents_online: number
  active_rooms: number
  participants: number
  closed: boolean
  messages_relayed: number
  bytes_relayed: number
  joins_total: number
  rooms_created: number
  auth_requests: number
  auth_denied: number
}

export interface ParticipantInfo {
  id: string
  subject: string
  remote_addr: string
  user_agent: string
  joined_at: string
}

export interface RoomInfo {
  id: string
  name: string
  key_id: string
  key_name: string
  created_at: string
  max_participants: number
  participant_count: number
  participants: ParticipantInfo[]
  agent_online: boolean
  messages: number
  bytes: number
}

export interface AgentInfo {
  agent_id: string
  key_id: string
  key_name: string
  remote_addr: string
  connected_at: string
  rooms: number
  participants: number
}

export interface EventItem {
  id: number
  time: string
  kind: string
  message: string
  room_id?: string
  subject?: string
}

export interface OverviewResponse {
  version: string
  started_at: string
  uptime_seconds: number
  stats: StatsSnapshot
  keys_total: number
  rooms: RoomInfo[]
  agents: AgentInfo[]
  events: EventItem[]
}

export interface KeyItem {
  id: string
  name: string
  prefix: string
  created_at: string
  last_used_at: string | null
  agent_online: boolean
  rooms: number
  participants: number
}

export interface KeysResponse {
  keys: KeyItem[]
}

export interface SessionInfo {
  authenticated: boolean
  setup_required: boolean
  env_managed: boolean
  version: string
}

export interface CreateKeyResponse {
  key: KeyItem
  secret: string
}

export interface RoomsResponse {
  rooms: RoomInfo[]
}

export interface RoomResponse {
  room: RoomInfo
}
