# curl 控制面示例

curl 适合验证 HTTP 控制面和排查问题。用户连接与使用方通道是长连接 WebSocket，需要 SDK 或 WebSocket 客户端。

## 环境

```sh
export XR_SERVER="http://127.0.0.1:8080"
export XR_API_KEY="你的 API Key"
```

## 公开接口

无需 API Key：

```sh
curl -sS "$XR_SERVER/api/sdk/version"
curl -sS -o XiaoyuRepeater_SDK_v1.zip "$XR_SERVER/api/sdk/download"
```

下载包内的 `config.json` 会写入本次请求使用的中继地址。

## 创建房间

```sh
curl -sS -X POST "$XR_SERVER/v1/rooms" \
  -H "Authorization: Bearer $XR_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"name":"demo room","max_participants":20}'
```

`name` 与 `max_participants` 都可省略；省略时由中继使用默认值。未知字段、空请求体或格式错误会返回 400。

响应节选（`webrtc_*` 仅在启用 WebRTC 时出现）：

```json
{
  "protocol": 1,
  "room": {
    "id": "r_xxxxxxxxx",
    "name": "demo room",
    "participant_count": 0
  },
  "ws_path": "/v1/rooms/r_xxxxxxxxx/ws",
  "ws_url": "ws://127.0.0.1:8080/v1/rooms/r_xxxxxxxxx/ws"
}
```

分发前把 `ws_url` 或 `webrtc_url` 发给用户，并追加自己的鉴权参数：

```
ws://127.0.0.1:8080/v1/rooms/r_xxxxxxxxx/ws?token=短期令牌&name=Alice
```

## 列出房间

```sh
curl -sS "$XR_SERVER/v1/rooms" \
  -H "X-Api-Key: $XR_API_KEY"
```

响应为 `{"rooms":[...]}`，只包含当前密钥创建的房间。

## 关闭房间

```sh
curl -sS -X DELETE "$XR_SERVER/v1/rooms/r_xxxxxxxxx" \
  -H "Authorization: Bearer $XR_API_KEY"
```

成功返回 200 与 `{"ok":true}`。房间不存在、已关闭或不属于当前密钥时返回 404（错误体只有 `error`，没有 `code`）；SDK 会把该 404 视为关闭成功，curl 需要自行判断。

## 使用方通道

使用方通道需要 WebSocket 客户端。以 websocat 为例（需另行安装）：

```sh
AGENT_URL="${XR_SERVER/http:/ws:}/v1/agent"
websocat -H="Authorization: Bearer $XR_API_KEY" "$AGENT_URL"
```

`https://` 地址不会被上面的替换改成 `wss://`，请手动改为 `wss://` 后再连接。

连接后先收到 `hello`。用户加入时会收到 `auth_request`，必须在超时前回复 `auth_response`。服务端每 20 秒发送 WebSocket 协议层 Ping，75 秒无读取活动会断开。完整控制消息见包根目录 `README.md`。

## 错误响应

普通错误只含 `error`：

```json
{ "error": "API Key 无效" }
```

房间、鉴权等控制面错误还会附带 `code`：

```json
{ "error": "房间人数已满", "code": "room_full" }
```

常见状态码：

| 状态码 | `code` | 含义 |
| --- | --- | --- |
| `400` | `invalid_max_participants` | `max_participants` 不在允许范围内 |
| `400` | 无 | 请求体为空、格式错误、包含未知字段或过大 |
| `401` | 无 | 缺少、无效或已撤销的 API Key |
| `403` | `auth_denied` | 用户鉴权被拒绝（用户侧） |
| `404` | `room_not_found` | 用户加入时房间不存在 |
| `404` | 无 | 关闭房间时房间不存在、已关闭或不属于当前密钥 |
| `409` | `room_full` | 房间人数已满 |
| `410` | `room_closed` | 房间已关闭 |
| `429` | `too_many_rooms` | 该密钥创建的房间数量已达上限 |
| `499` | `client_canceled` | 用户等待鉴权期间断开 |
| `503` | `agent_offline` | 使用方通道离线 |
| `503` | `agent_busy` | 使用方鉴权请求过多 |
| `503` | `webrtc_unavailable` | WebRTC 数据面未启用 |
| `503` | `server_shutting_down` | 服务正在关闭 |
| `504` | `auth_timeout` | 使用方在超时前未回复鉴权结果 |
