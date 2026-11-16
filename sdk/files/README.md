# XiaoyuRepeater SDK v1

面向使用方程序的客户端 SDK 包。使用方程序用 API Key 创建中转房间，把房间地址分发给自己的用户；用户加入时，中继通过双向 WebSocket 通道向使用方程序发起实时鉴权。通过后，用户与中继之间直接传输 WebSocket 帧或 WebRTC RTP 轨道。

本 SDK 只覆盖控制面：房间管理、使用方通道、鉴权回调、房间事件和轨道订阅。用户数据与媒体内容不经过 SDK，也不被中继解释。

## 包内结构

```
.
├── README.md              本文件
├── VERSION                SDK 版本号
├── LICENSE                MIT 许可证（中英双语）
├── config.json            中继地址，可手动修改
├── go/                    Go 1.24+
│   ├── README.md
│   ├── go.mod
│   ├── go.sum
│   ├── types.go
│   ├── client.go
│   ├── agent.go
│   └── cmd/consumer/      可运行示例
├── node/                  Node.js 18+
│   ├── README.md
│   ├── package.json
│   ├── xiaoyurepeater-sdk-v1.mjs
│   └── consumer.mjs
├── python/                Python 3.10+
│   ├── README.md
│   ├── requirements.txt
│   ├── xiaoyurepeater_sdk_v1.py
│   └── consumer.py
└── curl/                  HTTP 控制面示例
    └── README.md
```

## 快速开始

1. 解压 SDK 包。
2. `config.json` 默认指向 `http://127.0.0.1:8080`。从控制面板下载时，该文件自动写入当前中继地址；也可以手动修改。
3. 设置 API Key 环境变量：

```sh
export XR_API_KEY="你的 API Key"
# 如未修改 config.json，可额外设置：
export XR_SERVER="https://relay.example.com"
```

4. 进入任一语言目录运行示例。示例会创建房间、打印用户加入地址，并保持使用方通道在线：

```sh
# Go
cd go && go run ./cmd/consumer

# Node.js
cd node && npm install && node consumer.mjs

# Python
cd python && python3 -m venv .venv && . .venv/bin/activate
pip install -r requirements.txt
python consumer.py
```

5. 用示例打印的地址连接用户端。示例按 `token` 查询参数做演示鉴权；生产环境应换成自己的令牌或票据校验。

## 工作流程

```
使用方程序 ──创建房间 / Agent 通道──▶ 中继控制面
     ▲                                  │
     │ auth_request / auth_response     │ 用户加入
     └──────────────────────────────────┘
终端用户 ────── WebSocket / WebRTC ─────▶ 中继数据面
```

1. 使用方程序用 API Key 调用 `POST /v1/rooms` 创建房间，得到 `ws_url` 和可选的 `webrtc_url`。
2. 使用方程序把房间地址分发给自己的用户，并在地址中携带自己的鉴权参数。
3. 使用方程序连接 `GET /v1/agent`，保持通道在线。
4. 用户加入时，中继发送 `auth_request`，使用方程序在超时前回复 `auth_response`。
5. 通过后，用户加入房间，使用方程序收到 `user_joined`；用户离开时收到 `user_left`。
6. 使用方程序关闭房间或用户主动退出后，相应连接结束。

使用方通道断开后，SDK 会自动重连；重连成功会重新收到 `hello` 全量快照，使用方程序可据此恢复自己的房间状态。房间本身在中继侧保持运行，不会因使用方通道短暂断开而销毁。

## 环境变量

| 变量 | 用途 |
| --- | --- |
| `XR_SERVER` | 中继地址，覆盖 `config.json` 的 `baseUrl` |
| `XR_API_KEY` | API Key，避免把密钥写进代码或仓库 |

查找顺序：API Key 为显式参数 > `XR_API_KEY`；服务地址为显式参数 > `XR_SERVER` > SDK 根目录 `config.json`（Go SDK 还会尝试当前工作目录及其上级目录）。API Key 只放在服务端环境中，绝不能分发到终端用户。

## HTTP 控制面

`/v1` 下的使用方接口使用以下任一请求头：

```
Authorization: Bearer <API Key>
X-Api-Key: <API Key>
```

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| `GET` | `/api/sdk/version` | 查询 SDK 元数据，无需 API Key |
| `GET` | `/api/sdk/download` | 下载 SDK 包，无需 API Key |
| `POST` | `/v1/rooms` | 创建房间，请求体 `{ "name": "...", "max_participants": 20 }` |
| `GET` | `/v1/rooms` | 列出当前密钥的房间 |
| `DELETE` | `/v1/rooms/{id}` | 关闭自己的房间；房间不存在、已关闭或不属于当前密钥时返回 404，SDK 将该情况视为成功 |
| `GET` (WebSocket) | `/v1/agent` | 使用方控制通道 |

错误响应包含 `error`，带错误码的失败还会附带 `code`：

```json
{ "error": "错误说明", "code": "auth_denied" }
```

## 使用方通道

服务端发送（事件与应答）：

| 类型 | 说明 |
| --- | --- |
| `hello` | 连接建立后的全量快照：密钥、房间、在线连接、轨道、限制 |
| `auth_request` | 用户加入请求，转发用户的查询参数与请求头 |
| `user_joined` / `user_left` | 用户加入 / 离开 |
| `room_closed` | 房间关闭 |
| `track_published` / `track_unpublished` | WebRTC 轨道发布 / 结束 |
| `subscriptions_updated` | 订阅命令确认 |
| `error` | 命令执行失败 |
| `pong` | 对 JSON `ping` 的应答 |

使用方程序发送（命令与心跳）：

| 类型 | 说明 |
| --- | --- |
| `auth_response` | 对一次 `auth_request` 的放行或拒绝 |
| `subscriptions` | 设置某个 WebRTC 参与者的完整订阅列表 |
| `ping` | 可选的应用层心跳；中继收到后回复 `pong` |

服务端每 20 秒发送 WebSocket 协议层 Ping，客户端需正常回应 Pong；75 秒无读取活动会断开。SDK 使用的 WebSocket 客户端会自动处理协议层 Ping/Pong。

鉴权请求示例：

```json
{
  "type": "auth_request",
  "request_id": "aq_xxxxxxxxx",
  "room_id": "r_xxxxxxxxx",
  "room_name": "demo",
  "transport": "ws",
  "remote_addr": "203.0.113.10",
  "query": { "token": ["..."] },
  "headers": { "User-Agent": ["..."] },
  "requested_at": "2026-01-01T00:00:00Z"
}
```

放行响应：

```json
{
  "type": "auth_response",
  "request_id": "aq_xxxxxxxxx",
  "allow": true,
  "subject": "控制台显示名"
}
```

拒绝响应：

```json
{
  "type": "auth_response",
  "request_id": "aq_xxxxxxxxx",
  "allow": false,
  "reason": "令牌无效"
}
```

`subject` 只是显示标签，不参与路由或权限判断；`allow` 必须为 `true` 才放行（缺失按拒绝处理）。中继会移除 `Cookie` 与 WebSocket 握手头，避免控制面板会话泄漏给使用方程序；其余查询参数与请求头会在预算内转交：查询参数和请求头各自最多 32 个键、单值最多 2048 字节、各自总量最多 16KB，超出部分会被截断或丢弃。使用方程序必须自行校验。

## 数据面与 WebRTC

- WebSocket 用户之间发送的文本帧和二进制帧由中继原样转发，不解析格式；单帧大小受 `max_frame_bytes` 限制，超过上限会断开该用户连接。
- WebRTC 使用房间级 SFU 转发 RTP 包，不转码、不录制。
- 使用方通道只承载控制消息，不承载用户数据或媒体。
- 轨道元数据只用于选择订阅，中继不据此改变载荷。

订阅命令的语义是“替换完整列表”：

```json
{
  "type": "subscriptions",
  "request_id": "sub_xxx",
  "room_id": "r_xxxxxxxxx",
  "conn_id": "p_xxxxxxxx",
  "track_ids": ["t_aaaaaaaa", "t_bbbbbbbb"],
  "all": false
}
```

`all: true` 表示订阅当前及未来全部轨道，此时忽略 `track_ids`；空数组表示不订阅任何轨道。未发送过订阅命令的参与者默认订阅全部轨道。订阅只决定该参与者接收哪些轨道（不会收到自己发布的轨道）；首次协商完成前设置的订阅会在协商完成后生效。SDK 会等待 `subscriptions_updated` 或 `error` 后返回；等待期间通道断开则返回断开错误。

## 重连与关闭码

SDK 在普通断开后自动重连：初始等待 1 秒，未收到 `hello` 的连续失败按 2 倍退避，最长 30 秒；成功收到 `hello` 后重置。以下情况终止重连：

| 关闭码 | 含义 |
| --- | --- |
| `4001` | 同一 API Key 的新使用方通道替换了当前连接 |
| `4003` | API Key 已被撤销 |
| HTTP `401` | 握手阶段密钥无效 |

其他关闭码与网络错误按可恢复处理。房间关闭时用户连接以 `4000` 关闭；升级后加入失败使用 `4004`；慢消费者使用 `1008`；服务关闭等场景使用 `1001` 等标准关闭码。

## 安全建议

- API Key 只保存在使用方程序的服务端，不下发到浏览器或终端用户。
- 用户鉴权参数使用短期令牌，并在使用方程序中校验有效期、房间范围和权限。
- 生产环境使用 HTTPS，SDK 会自动把 `https://` 转为 `wss://`。
- 不要把含令牌的查询参数写入日志；中继日志也不会记录查询内容。
- 鉴权处理器应设置自己的超时与并发上限，避免慢请求堆积。
- 房间关闭后及时释放本地状态；收到 `hello` 快照后以快照为准做对账。

## 版本

- SDK 版本：`1`（见 `VERSION`）
- 控制协议版本：`1`
- 版本查询：`GET /api/sdk/version`
- 下载地址：`GET /api/sdk/download`，文件名为 `XiaoyuRepeater_SDK_v1.zip`

许可证：MIT，版权归重庆沛雨霖科技有限公司（PYLinTech）所有，详见 `LICENSE`。
