# 控制协议与转发语义

协议版本：**1**。所有控制消息均为 UTF-8 JSON 文本帧。WebSocket 数据面帧不做任何解析；WebRTC 数据面只读取 RTP 头做转发路由，payload 原样透传。

中继分为两层：

- **控制面**：API 密钥、房间生命周期、加入前鉴权、加入/离开事件。
- **数据面**：房间内用户之间的 WebSocket 帧原样转发，以及可选的自托管 WebRTC SFU 轨道转发。中继不读取、不记录、不改写业务内容，也不把用户数据转发给使用方程序。

---

## 1. 使用方身份

使用方调用 HTTP API 与连接鉴权通道时，必须携带 API 密钥，二选一：

```http
Authorization: Bearer <API密钥>
```

```http
X-Api-Key: <API密钥>
```

密钥在控制台创建时只完整显示一次，服务端只保存哈希。密钥无效返回 `401`。

---

## 2. 房间管理

### 2.1 创建房间

```http
POST /v1/rooms
Authorization: Bearer <API密钥>
Content-Type: application/json
```

```json
{
  "name": "演示房间",
  "max_participants": 20
}
```

| 字段 | 必填 | 说明 |
| --- | --- | --- |
| `name` | 否 | 房间名，最长 80 字符，默认「未命名房间」 |
| `max_participants` | 否 | 人数上限，默认取 `XR_DEFAULT_MAX_PARTICIPANTS`，范围 2–`XR_MAX_PARTICIPANTS` |

请求体不接受未定义字段。成功返回 `201`：

```json
{
  "protocol": 1,
  "room": {
    "id": "r_xxxxxxxxx",
    "name": "演示房间",
    "key_id": "k_xxxxxxxx",
    "key_name": "会议分发",
    "created_at": "2026-11-16T10:00:00Z",
    "max_participants": 20,
    "participant_count": 0,
    "participants": [],
    "agent_online": true,
    "rtc_peers": 0,
    "tracks": 0,
    "messages": 0,
    "bytes": 0,
    "rtp_packets": 0,
    "rtp_bytes": 0
  },
  "ws_path": "/v1/rooms/r_xxxxxxxxx/ws",
  "ws_url": "ws://127.0.0.1:8080/v1/rooms/r_xxxxxxxxx/ws",
  "webrtc_path": "/v1/rooms/r_xxxxxxxxx/webrtc",
  "webrtc_url": "ws://127.0.0.1:8080/v1/rooms/r_xxxxxxxxx/webrtc"
}
```

`ws_url`、`webrtc_url` 依据请求 Host 与 HTTPS 状态生成；若服务在反向代理后，需正确配置 `XR_TRUST_PROXY` 与转发头。WebRTC 数据面关闭时（`XR_WEBRTC_ENABLED=false`），响应中不包含 `webrtc_path` 与 `webrtc_url`。

单密钥房间数达到 `XR_MAX_ROOMS_PER_KEY` 时返回 `429 too_many_rooms`。

### 2.2 列出与关闭房间

```http
GET /v1/rooms
Authorization: Bearer <API密钥>
```

返回当前密钥名下所有房间（结构同上方 `room`）。房间在使用方通道断开后仍然存在，便于重连后继续使用。

```http
DELETE /v1/rooms/{id}
Authorization: Bearer <API密钥>
```

只能关闭自己名下的房间。关闭后：

- 在线用户收到关闭码 `4000`，原因 `closed_by_consumer`；
- 使用方通道收到 `room_closed` 事件；
- 房间从列表移除，之后加入返回 `404 room_not_found`。

---

## 3. 使用方鉴权通道

### 3.1 建立连接

```http
GET /v1/agent
Upgrade: websocket
Authorization: Bearer <API密钥>
```

每个 API 密钥同时只保留一条鉴权通道。同一密钥的新连接会替换旧连接，旧连接收到关闭码 `4001`（`replaced by new connection`），未完成的鉴权请求按「使用方未连接」失败。

连接成功后，服务端首先发送 `hello`：

```json
{
  "type": "hello",
  "protocol": 1,
  "agent_id": "a_xxxxxxxx",
  "key_id": "k_xxxxxxxx",
  "key_name": "会议分发",
  "server_time": "2026-11-16T10:00:00Z",
  "rooms": [
    {
      "id": "r_xxxxxxxxx",
      "name": "演示房间",
      "participants": 2,
      "created_at": "2026-11-16T09:59:00Z"
    }
  ],
  "limits": {
    "max_rooms": 100,
    "max_frame_bytes": 1048576,
    "auth_timeout_ms": 10000,
    "max_participants_limit": 500,
    "webrtc": true,
    "max_tracks_per_peer": 8
  }
}
```

`rooms` 是该密钥名下仍存活的房间，使用方可据此恢复业务状态。服务端每 20 秒发送 WebSocket Ping，客户端需正常回应 Pong；75 秒无读取活动会断开。

### 3.2 鉴权请求

用户请求加入时，服务端先做房间存在性、容量与使用方在线检查，然后向使用方发送：

```json
{
  "type": "auth_request",
  "request_id": "aq_xxxxxxxxx",
  "room_id": "r_xxxxxxxxx",
  "room_name": "演示房间",
  "transport": "ws",
  "remote_addr": "203.0.113.10",
  "query": {
    "token": ["用户带来的值"],
    "name": ["Alice"]
  },
  "headers": {
    "Authorization": ["Bearer 用户令牌"],
    "User-Agent": ["..."]
  },
  "requested_at": "2026-11-16T10:00:05Z"
}
```

转发给使用方的用户请求信息：

- `transport` 为 `ws`（WebSocket 帧通道）或 `webrtc`（WebRTC 媒体通道）；使用方可据此采用不同的鉴权策略。
- 保留用户 WS 握手请求的 query 与请求头，供使用方自行鉴权。
- **始终剔除 `Cookie`** 与 WebSocket 握手头（`Sec-WebSocket-*`、`Connection`、`Upgrade` 等），避免控制台会话泄漏。
- `Authorization` 会原样转发，使用方可用它承载自己的用户令牌。
- 预算：query 与 headers 各自最多 32 个键、单值最多 2048 字节、总计最多 16KB，超出部分可能被截断或丢弃。
- 服务端不记录这些值。

使用方必须在 `XR_AUTH_TIMEOUT`（默认 10 秒）内回复同一 `request_id`。

### 3.3 鉴权结果

放行：

```json
{
  "type": "auth_response",
  "request_id": "aq_xxxxxxxxx",
  "allow": true,
  "subject": "Alice"
}
```

拒绝：

```json
{
  "type": "auth_response",
  "request_id": "aq_xxxxxxxxx",
  "allow": false,
  "reason": "令牌已过期"
}
```

| 字段 | 说明 |
| --- | --- |
| `allow` | 必填，`true` 放行，`false` 或缺失视为拒绝 |
| `subject` | 可选，仅作为控制台显示名；为空时使用「用户」。中继不会用它做权限或内容判断 |
| `reason` | 可选，拒绝原因，会拼入返回给用户的 403 文案 |

超时未回复返回 `504 auth_timeout`；通道断开返回 `503 agent_offline`；使用方待处理请求过多返回 `503 agent_busy`。

### 3.4 控制事件

用户加入后，使用方收到：

```json
{
  "type": "user_joined",
  "room_id": "r_xxxxxxxxx",
  "room_name": "演示房间",
  "conn_id": "c_xxxxxx",
  "subject": "Alice",
  "transport": "ws",
  "remote_addr": "203.0.113.10",
  "joined_at": "2026-11-16T10:00:06Z"
}
```

用户离开：

```json
{
  "type": "user_left",
  "room_id": "r_xxxxxxxxx",
  "room_name": "演示房间",
  "conn_id": "c_xxxxxx",
  "subject": "Alice",
  "transport": "ws",
  "reason": "client_closed",
  "left_at": "2026-11-16T10:03:00Z"
}
```

`reason` 常见值：`client_closed`、`disconnected`、`timeout`、`slow_consumer`。

房间关闭：

```json
{
  "type": "room_closed",
  "room_id": "r_xxxxxxxxx",
  "room_name": "演示房间",
  "reason": "closed_by_admin",
  "closed_at": "2026-11-16T10:05:00Z"
}
```

`reason`：`closed_by_admin`、`closed_by_consumer`、`key_revoked`、`server_shutdown`。

控制通道还支持 JSON 心跳消息 `ping` / `pong`（`{"type":"ping","time":"..."}`），收到 `ping` 应回复 `pong`；WebSocket 协议层 Ping/Pong 同时生效。

### 3.5 断开与密钥撤销

- 使用方通道断开后，房间保留；新用户加入返回 `503 agent_offline`，已有用户连接不受影响。
- 使用方重连后会收到包含现有房间的 `hello`。
- 在控制台删除 API 密钥会立即关闭该密钥的使用方通道（关闭码 `4003`）并关闭其全部房间（用户关闭码 `4000`，原因 `key_revoked`）。

---

## 4. 用户加入房间

```http
GET /v1/rooms/{id}/ws
Upgrade: websocket
```

用户可在 query 或请求头中携带任意鉴权信息，由使用方程序决定是否放行；中继自身不解析用户令牌。

### 4.1 升级前检查顺序

1. 房间不存在 → `404 room_not_found`
2. 房间已关闭 → `410 room_closed`
3. 房间已满 → `409 room_full`
4. 使用方通道不在线 → `503 agent_offline`
5. 使用方待处理鉴权请求过多 → `503 agent_busy`
6. 等待使用方回复超时 → `504 auth_timeout`
7. 使用方拒绝 → `403 auth_denied`（可带具体原因）
8. 鉴权通过但升级瞬间房间满/关闭 → `409` / `410`

错误响应：

```json
{
  "error": "鉴权未通过：令牌已过期",
  "code": "auth_denied"
}
```

### 4.2 升级成功

响应头：

```http
X-Repeater-Room: r_xxxxxxxxx
X-Repeater-Subject: <URL 编码的显示名>
X-Repeater-Transport: ws
```

随后连接进入房间。用户帧只在房间内转发，不经过使用方通道。

### 4.3 用户连接关闭码

| 关闭码 | 含义 |
| --- | --- |
| `1000` | 正常关闭 |
| `1001` | 服务正在关闭 |
| `1008` | 发送队列积压，接收端过慢 |
| `4000` | 房间被关闭；reason 见 `room_closed` |
| `4004` | 升级后加入失败（房间关闭/已满） |

WebRTC 信令连接使用同一套关闭码；此外，服务端在信令写队列溢出时以 `1008` 关闭，在信令 offer 超时或 SDP/ICE 失败时关闭 PeerConnection 并断开信令连接。

---

## 5. WebRTC 媒体通道

服务端内置可选的 WebRTC SFU（Pion），不依赖外部信令、STUN、TURN 或媒体服务。每个通过鉴权的用户建立一条独立 PeerConnection，发布的 RTP 轨道进入房间，由服务端转发给房间内其他参与者的 PeerConnection。

由 `XR_WEBRTC_ENABLED` 控制（默认开启）。关闭时加入地址直接返回 `503 webrtc_unavailable`（不触发使用方鉴权），创建房间响应不包含 `webrtc_path` / `webrtc_url`。

### 5.1 加入与鉴权

```http
GET /v1/rooms/{id}/webrtc
Upgrade: websocket
```

用户在 query 或请求头中携带任意鉴权信息，规则与 WebSocket 用户完全一致：先做房间存在性、容量、使用方在线检查，再向使用方发送 `auth_request`，其中 `transport` 为 `webrtc`。拒绝、超时、房间已满等错误与第 4.1 节相同。

鉴权通过后完成 WebSocket 升级，响应头：

```http
X-Repeater-Room: r_xxxxxxxxx
X-Repeater-Subject: <URL 编码的显示名>
X-Repeater-Transport: webrtc
```

随后该 WebSocket 只承载 WebRTC 信令，不转发任何业务字节。WS 用户与 WebRTC 参与者共享同一个房间人数上限和参与者列表。

### 5.2 信令格式

信令为 UTF-8 JSON 文本帧，双向使用同一结构：

```json
{
  "type": "offer",
  "sdp": "v=0\r\n..."
}
```

```json
{
  "type": "answer",
  "sdp": "v=0\r\n..."
}
```

```json
{
  "type": "ice",
  "candidate": {
    "candidate": "candidate:1 1 udp 2130706431 203.0.113.10 50000 typ host",
    "sdp_mid": "0",
    "sdp_mline_index": 0,
    "username_fragment": "xxxx"
  }
}
```

```json
{
  "type": "track_added",
  "track": {
    "id": "t_xxxxxxxx",
    "kind": "audio",
    "stream_id": "microphone",
    "codec": "audio/opus",
    "publisher_id": "p_xxxxxxxx",
    "subject": "Alice"
  }
}
```

```json
{
  "type": "track_removed",
  "track": {
    "id": "t_xxxxxxxx",
    "kind": "audio",
    "stream_id": "microphone",
    "codec": "audio/opus",
    "publisher_id": "p_xxxxxxxx",
    "subject": "Alice"
  }
}
```

```json
{
  "type": "bye"
}
```

```json
{
  "type": "error",
  "error": "发布轨道数量超过上限"
}
```

| `type` | 方向 | 说明 |
| --- | --- | --- |
| `offer` | 双向 | SDP offer；客户端必须首先发送自己的首个 offer |
| `answer` | 双向 | 对收到的 offer 的应答 |
| `ice` | 双向 | Trickle ICE 候选；可在远端描述设置前提前到达，服务端会暂存 |
| `track_added` | 服务端 → 用户 | 服务端已把某条房间轨道挂到该连接，随后会发起重协商 |
| `track_removed` | 服务端 → 用户 | 轨道已停止（发布者离开或取消发布），随后会发起重协商 |
| `bye` | 用户 → 服务端 | 主动优雅离开；服务端关闭 PeerConnection 与信令连接 |
| `error` | 服务端 → 用户 | 信令错误或轨道数量超限；是否断开取决于错误类型 |

`kind`、`codec`、`stream_id`、`rid` 均为发布端透传的元数据，中继不据此改变媒体内容。

### 5.3 协商流程

1. 客户端创建 PeerConnection，按需添加发送轨道（`addTrack`）或接收方向（`recvonly` transceiver）。
2. 客户端发送首个 `offer`；服务端回 `answer`。
3. 服务端在首个 offer/answer 完成后，把房间内已有轨道挂到该连接，并发送 `track_added`，随后主动发送 `offer`；客户端回 `answer`。
4. 后续任何一方新增轨道都会触发服务端重协商：服务端发送 `offer`，客户端回 `answer`。
5. ICE 候选可随时双向发送。服务端 offer 在 15 秒内未收到 answer 会关闭该连接。

注意事项：

- 服务端不会先于客户端发起首个 offer；客户端必须主动发送 offer，否则无法建立媒体连接。
- 若客户端在服务端 offer 尚未应答时发送新的 offer，服务端会回滚自己的本地 offer、处理客户端 offer，并在信令稳定后重新发起服务端 offer。
- 房间内所有已发布轨道都会转发给每个其他参与者，当前不提供按轨道订阅/取消订阅的接口；客户端可通过 SDP 的 direction 自行忽略不接收的轨道，或直接不读取该 Track。
- 每个参与者最多发布 `XR_WEBRTC_MAX_TRACKS_PER_PEER` 条轨道（默认 8，范围 1–32），超限时服务端发送 `error` 并忽略后续轨道。

### 5.4 媒体转发语义

- 每条发布轨道对应一个房间级轨道；每个订阅者获得独立的 `TrackLocalStaticRTP`，服务端按 RTP 包逐包复制。
- 只解析 RTP 头用于路由与统计，payload、扩展头内容、编码参数均原样透传；不解码、不转码、不混流、不录制、不落盘。
- 服务端不实现业务层订阅、发言权、静音或内容规则；这些由使用方与用户自行约定。
- 订阅者发送的 PLI/FIR 会转发给发布者以请求关键帧，服务端不做关键帧缓存。
- 未知轨道/未知信令类型会被忽略；非法 JSON 或无效 SDP 会返回 `error`。
- 媒体数据只在 PeerConnection 的 UDP 通道传输，不经过信令 WebSocket，也不经过使用方通道。

### 5.5 网络与部署

- 服务端默认在 `50000–50100/udp` 范围内为每个 PeerConnection 分配端口，需在防火墙与容器映射中放行。端口范围决定可并发的媒体连接数，房间规模较大时应同步扩大范围。
- 服务端只使用本机候选地址；当服务位于 1:1 NAT 或容器后，用 `XR_WEBRTC_PUBLIC_IP` 填写对外可达 IP，服务端会重写候选地址。多个 IP 用逗号分隔。
- 不内置 STUN/TURN。客户端需能直接访问服务端的 UDP 端口；强制对称 NAT、仅允许 443/TCP 的网络可能无法建立媒体连接。
- 反向代理只代理 HTTP 与 WebSocket；WebRTC 的 UDP 流量不经过反向代理，必须由主机直接暴露端口。

---

## 6. 数据面语义

鉴权通过并完成 WebSocket 升级后：

- 仅转发 **Text** 与 **Binary** 帧；消息类型保持不变。
- 发送者的帧会复制给房间内**其他所有**用户连接，发送者不会收到自己的回声。
- 中继不解析 payload、不区分 JSON/媒体/控制指令，也不做合并、拆分、转码、落盘或内容审核。
- 单帧上限为 `XR_MAX_FRAME_BYTES`（默认 1 MiB）；超限连接会被关闭。
- 每个用户连接有容量 256 的发送队列。队列满表示该连接消费过慢，会被关闭（`1008 slow consumer`），不影响其他用户。
- 同一发送者的帧按发送顺序进入各接收队列；不同发送者之间不提供全局顺序保证。
- 中继按「每次转发」统计消息数与字节数，不按接收人数重复计数；统计数据仅用于控制台展示。
- 服务端不持久化任何转发内容；房间与连接状态仅存在于内存。

WebRTC 数据面见第 5.4 节：RTP 包按轨道逐包转发，统计字段为 `rtp_packets` / `rtp_bytes`，与 WebSocket 的 `messages` / `bytes` 分开累计。

---

## 7. 管理接口

管理接口位于 `/api/admin/*`，使用 `xr_admin` 会话 Cookie（`HttpOnly`、`SameSite=Strict`，HTTPS 下 `Secure`）。除 `session`、首次 `setup`、`login` 外均需登录；写操作校验同源 `Origin`。

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| `GET` | `/api/admin/session` | 登录状态、是否需要初始化、版本 |
| `POST` | `/api/admin/setup` | 首次设置管理密码 `{password}` |
| `POST` | `/api/admin/login` | 登录 `{password}`，8 次失败/5 分钟/IP 限速 |
| `POST` | `/api/admin/logout` | 退出登录 |
| `PUT` | `/api/admin/password` | 修改密码 `{current_password,new_password}`，环境变量管理时禁用 |
| `GET` | `/api/admin/overview` | 运行统计、房间、使用方连接、事件 |
| `GET` | `/api/admin/rooms` | 房间列表 |
| `GET` | `/api/admin/rooms/{id}` | 房间详情 |
| `DELETE` | `/api/admin/rooms/{id}` | 关闭房间 |
| `GET` | `/api/admin/keys` | 密钥列表（仅前缀与状态） |
| `POST` | `/api/admin/keys` | 创建密钥 `{name}`，响应包含一次性 `secret` |
| `DELETE` | `/api/admin/keys/{id}` | 删除密钥并关闭其房间 |

`GET /healthz` 无需鉴权，用于健康检查。

`overview`、房间列表与房间详情的统计包含 WS 与 WebRTC 两部分：`rtc_peers`（WebRTC 参与人数）、`tracks`（房间内已发布轨道数）、`rtp_packets` / `rtp_bytes`（RTP 转发量）。参与者条目带 `transport`（`ws` / `webrtc`），WebRTC 参与者另有 `tracks`（本人发布的轨道数）。

---

## 8. 使用方示例

完整可运行示例见 [`examples/consumer`](../examples/consumer/main.go)：创建房间、建立鉴权通道、按演示令牌放行、处理事件、断线重连与退出时关闭房间。示例仅演示控制面流程；用户之间的数据由用户自行收发，中继原样转发。
