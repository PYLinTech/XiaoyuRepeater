# 控制协议与转发语义

协议版本：**1**。所有控制消息均为 UTF-8 JSON 文本帧；数据面帧不做任何解析。

中继分为两层：

- **控制面**：API 密钥、房间生命周期、加入前鉴权、加入/离开事件。
- **数据面**：房间内用户之间的 WebSocket 帧原样转发。中继不读取、不记录、不改写帧内容，也不把数据转发给使用方程序。

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
    "messages": 0,
    "bytes": 0
  },
  "ws_path": "/v1/rooms/r_xxxxxxxxx/ws",
  "ws_url": "ws://127.0.0.1:8080/v1/rooms/r_xxxxxxxxx/ws"
}
```

`ws_url` 依据请求 Host 与 HTTPS 状态生成；若服务在反向代理后，需正确配置 `XR_TRUST_PROXY` 与转发头。

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
    "max_participants_limit": 500
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

---

## 5. 数据面语义

鉴权通过并完成 WebSocket 升级后：

- 仅转发 **Text** 与 **Binary** 帧；消息类型保持不变。
- 发送者的帧会复制给房间内**其他所有**用户连接，发送者不会收到自己的回声。
- 中继不解析 payload、不区分 JSON/媒体/控制指令，也不做合并、拆分、转码、落盘或内容审核。
- 单帧上限为 `XR_MAX_FRAME_BYTES`（默认 1 MiB）；超限连接会被关闭。
- 每个用户连接有容量 256 的发送队列。队列满表示该连接消费过慢，会被关闭（`1008 slow consumer`），不影响其他用户。
- 同一发送者的帧按发送顺序进入各接收队列；不同发送者之间不提供全局顺序保证。
- 中继按「每次转发」统计消息数与字节数，不按接收人数重复计数；统计数据仅用于控制台展示。
- 服务端不持久化任何转发内容；房间与连接状态仅存在于内存。

---

## 6. 管理接口

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

---

## 7. 使用方示例

完整可运行示例见 [`examples/consumer`](../examples/consumer/main.go)：创建房间、建立鉴权通道、按演示令牌放行、处理事件、断线重连与退出时关闭房间。示例仅演示控制面流程；用户之间的数据由用户自行收发，中继原样转发。
