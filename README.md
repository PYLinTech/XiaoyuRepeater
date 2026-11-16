# XiaoyuRepeater

面向实时场景的通用中继服务。使用方程序凭 API 密钥创建中转房间，把房间地址分发给自己的用户；用户连接房间时，中继服务通过双向 WebSocket 实时询问使用方程序是否放行，通过后即可加入。

数据面提供两条可并行的通道：WebSocket 文本/二进制帧原样转发，以及可选的 WebRTC SFU 轨道转发。中继不解析业务内容、不落盘、不转码，也没有任何业务内容规则。内置 Go 服务端与 Vue 控制台，单个二进制即可部署。

## 适用场景

- 会议流分发、直播旁路分发
- 远程协助 / 远程控制信令与画面转发
- 其他需要“房间 + 使用方实时鉴权 + 透明转发”的实时应用

WS 通道适合信令、状态同步与任意字节流；WebRTC 通道适合低延迟音视频轨道。中继不包含编解码、混流、录制、内容审核等能力，具体传输内容完全由使用方与用户自行约定。

## 工作方式

```text
                     控制面（HTTP / WebSocket）
 使用方程序  ── 创建房间、关闭房间 ──────────────┐
     │                                          ▼
     │  WS /v1/agent（鉴权通道）          ┌───────────────┐
     └──── auth_request / auth_response ─│ XiaoyuRepeater │
                                         │   Go 服务端    │
 用户 A ── WS /v1/rooms/{id}/ws ────────▶│ WS 帧转发      │
 用户 B ── WS /v1/rooms/{id}/ws ────────▶│ RTP 轨道转发   │
 用户 C ── WS /v1/rooms/{id}/webrtc ────▶│ （WebRTC SFU） │
                                         └───────────────┘
                                                ▲
 管理员 ── 浏览器控制台（Vue）──────────────────┘
```

- **控制面**：API 密钥、房间生命周期、加入前鉴权、轨道订阅控制、事件通知。
- **数据面**：WS 用户之间的帧转发；WebRTC 参与者之间的 RTP 轨道转发。使用方程序只参与鉴权与控制事件，不接收转发内容。
- **鉴权在升级前完成**：未通过时直接返回 HTTP 403/409/503/504，不会建立用户连接。
- **可选 WebRTC**：默认启用、完全自托管，基于 Pion，不引入外部信令、STUN、TURN 或媒体服务。

## 快速开始

要求：Go 1.24+；构建控制台需要 Node.js 20.19+。

```bash
# 构建控制台并编译单文件服务端
make build

# 启动（默认 :8080，数据写入 ./data）
./bin/xiaoyurepeater
```

1. 打开 <http://127.0.0.1:8080>，首次使用先设置管理密码。
2. 在「API 密钥」页创建一个密钥，完整密钥只显示一次，请立即保存。
3. 启动示例使用方：

```bash
XR_API_KEY='<刚创建的密钥>' go run ./examples/consumer
```

示例会创建房间、保持鉴权通道，并打印用户加入地址。开两个终端用任意 WebSocket 客户端加入即可互相收发字节流，例如：

```bash
npx wscat -c 'ws://127.0.0.1:8080/v1/rooms/<房间ID>/ws?token=demo-token&name=Alice'
npx wscat -c 'ws://127.0.0.1:8080/v1/rooms/<房间ID>/ws?token=demo-token&name=Bob'
```

示例的鉴权规则仅为演示：`token=demo-token` 时放行，`name` 作为控制台显示名。真实使用方可以按自己的业务校验任意 query、请求头（例如短时令牌、Cookie 之外的签名头），只返回 `allow` 与可选显示名。

启用 WebRTC 时，创建房间响应还会附带 `webrtc_path` / `webrtc_url`。音视频客户端连接该地址并完成同一套实时鉴权后，即可按 [docs/protocol.md](docs/protocol.md) 第 5 节进行 SDP/ICE 信令与 RTP 轨道收发。

### SDK 包

控制台「SDK 下载」页提供使用方客户端包，内含 Go、Node.js、Python 与 curl 示例。包只覆盖控制面：房间管理、使用方通道、鉴权回调、事件与轨道订阅；不包含用户数据或媒体转发逻辑。

- 下载地址：`GET /api/sdk/download`，文件名为 `XiaoyuRepeater_SDK_v1.zip`
- 版本信息：`GET /api/sdk/version`，无需 API Key
- 下载的 `config.json` 会自动写入当前控制台地址；跨环境部署时可用 `XR_SERVER` 覆盖
- SDK 源码位于 `sdk/files/`；修改后运行 `go generate ./sdk` 重新生成嵌入归档 `sdk/XiaoyuRepeater_SDK_v1.zip`，再执行 `make build`

SDK 使用说明、目录结构与各语言快速开始见包内 `README.md`。

### 开发模式

```bash
# 终端 1：Go 服务
go run .

# 终端 2：Vite 开发服务器（/api、/v1 代理到 :8080）
cd web && npm install && npm run dev
```

## 配置

| 环境变量 | 默认值 | 说明 |
| --- | --- | --- |
| `XR_ADDR` | `:8080` | HTTP 监听地址 |
| `XR_DATA_DIR` | `./data` | 持久化目录，保存管理密码与 API 密钥哈希 |
| `XR_ADMIN_PASSWORD` | 空 | 设置后由环境变量管理管理密码，控制台隐藏修改入口；至少 8 字符 |
| `XR_TLS_CERT` / `XR_TLS_KEY` | 空 | 直接启用 HTTPS/WSS；两项必须同时配置 |
| `XR_TRUST_PROXY` | `false` | 信任 `X-Forwarded-For` / `X-Forwarded-Proto`，仅在上游反向代理后开启 |
| `XR_AUTH_TIMEOUT` | `10s` | 等待使用方鉴权结果的超时，范围 1s–2m |
| `XR_SESSION_TTL` | `12h` | 管理后台登录有效期，范围 1m–720h |
| `XR_MAX_ROOMS_PER_KEY` | `100` | 单个密钥可同时存在的房间数，1–10000 |
| `XR_DEFAULT_MAX_PARTICIPANTS` | `50` | 创建房间未指定人数上限时的默认值 |
| `XR_MAX_PARTICIPANTS` | `500` | 房间人数上限的最大值 |
| `XR_MAX_FRAME_BYTES` | `1048576` | 单帧最大字节数（1 MiB），1KB–64MB |
| `XR_WEBRTC_ENABLED` | `true` | 是否启用自托管 WebRTC SFU |
| `XR_WEBRTC_UDP_PORT_MIN` / `XR_WEBRTC_UDP_PORT_MAX` | `50000` / `50100` | WebRTC UDP 端口范围，1024–65535 |
| `XR_WEBRTC_PUBLIC_IP` | 空 | 1:1 NAT/容器对外的 IP，多个用逗号分隔；用于重写 ICE 候选 |
| `XR_WEBRTC_MAX_TRACKS_PER_PEER` | `8` | 每个 WebRTC 参与者最多发布的轨道数，1–32 |
| `XR_LOG_LEVEL` | `info` | `debug` / `info` / `warn` / `error` |

## HTTP / WebSocket 接口

使用方接口使用 `Authorization: Bearer <API密钥>`（也接受 `X-Api-Key`）。

| 方法 | 路径 | 用途 |
| --- | --- | --- |
| `POST` | `/v1/rooms` | 创建房间，返回 `ws_path` / `ws_url` |
| `GET` | `/v1/rooms` | 列出当前密钥名下的房间 |
| `DELETE` | `/v1/rooms/{id}` | 关闭自己的房间 |
| `WS` | `/v1/agent` | 使用方鉴权通道（每个密钥同时一个连接） |
| `WS` | `/v1/rooms/{id}/ws` | 用户加入房间（WebSocket 帧通道） |
| `WS` | `/v1/rooms/{id}/webrtc` | 用户加入房间（WebRTC 信令与媒体通道） |
| `GET` | `/api/sdk/version` | SDK 版本信息（公开） |
| `GET` | `/api/sdk/download` | 下载 SDK 包（公开） |
| `GET` | `/healthz` | 健康检查 |

管理接口位于 `/api/admin/*`，使用 HttpOnly 会话 Cookie；完整的控制协议、鉴权消息、错误码与转发语义见 [docs/protocol.md](docs/protocol.md)。

## 转发语义

### WebSocket 帧

- 只接受 WebSocket 文本帧与二进制帧；消息类型保持不变，原样发给房间内除发送者以外的所有连接。
- 中继不解析帧内容、不区分业务类型、不记录帧内容；只统计消息条数与字节数。
- 每个用户连接有独立发送队列。队列写满表示接收端过慢，该连接会被关闭（1008），不影响其他用户。
- 使用方程序不接收转发数据，只收到加入/离开/房间关闭等控制事件。
- 房间与在线连接保存在内存中，服务重启后房间消失；API 密钥与管理密码持久化在 `XR_DATA_DIR/state.json`。

### WebRTC 轨道

- 每个 WebRTC 参与者一条独立 PeerConnection；发布轨道进入房间后，默认转发给其他所有参与者。
- 服务端只读 RTP 头做路由与统计，payload、扩展与编码参数原样透传；不解码、不转码、不混流、不录制。
- 使用方可通过鉴权通道的 `subscriptions` 命令，为某个 WebRTC 参与者设置精确订阅列表或 `all=true`；中继只执行列表，不按轨道类型、发布者或业务含义自行选择。默认未设置时为全部订阅，详见 [docs/protocol.md](docs/protocol.md) 第 3.5 节。
- 轨道发布或结束时，使用方收到 `track_published` / `track_unpublished` 事件；重连后可从 `hello.rooms[].connections` 与 `hello.rooms[].tracks` 恢复在线连接与轨道快照。
- 订阅者的 PLI/FIR 会转发给发布者以请求关键帧；服务端不缓存关键帧。
- 每个参与者最多发布 `XR_WEBRTC_MAX_TRACKS_PER_PEER` 条轨道；超限时返回信令 `error` 并忽略后续轨道。
- WS 与 WebRTC 参与者共享房间人数上限与在线列表；两种通道互不影响，可在同一房间并行使用。

## WebRTC 数据面

WebRTC 使用 [Pion](https://github.com/pion/webrtc) 在进程内实现 SFU，完全自托管：

- 信令复用房间鉴权：连接 `/v1/rooms/{id}/webrtc`，通过使用方实时鉴权后升级为信令 WebSocket。
- 客户端必须先发送 SDP offer；服务端应答后，会对房间内已有轨道及后续新增轨道发起重协商。详细的信令 JSON 与协商顺序见 [docs/protocol.md](docs/protocol.md) 第 5 节。
- 媒体走 UDP，默认端口范围 `50000–50100`，需要放行防火墙与容器端口。反向代理只覆盖 HTTP/WS，不代理 WebRTC 的 UDP 流量。
- 服务端只广告本机候选；在 1:1 NAT 或容器后，通过 `XR_WEBRTC_PUBLIC_IP` 填写对外可达 IP。
- 不内置 STUN/TURN。客户端需要能直接访问服务端 UDP 端口；强制对称 NAT 或仅允许 443/TCP 的网络可能无法建立媒体连接。
- 设置 `XR_WEBRTC_ENABLED=false` 可完全关闭媒体面，此时创建房间不再返回 `webrtc_url`，媒体加入返回 `503 webrtc_unavailable`。

## 安全说明

- API 密钥只保存哈希，明文只在创建时显示一次，日志中不记录密钥与用户令牌。
- 管理密码使用 PBKDF2-SHA256（210,000 次迭代）；登录失败按 IP 限速。
- 管理员会话 Cookie 为 `HttpOnly` + `SameSite=Strict`；HTTPS 下自动加 `Secure`。
- 转发给使用方的用户请求会剔除 Cookie 与 WebSocket 握手头，避免控制台会话泄漏；query 与请求头各有 16KB 预算。
- 完整密钥、管理密码不要写入仓库或镜像；生产环境建议放在反向代理或直连 TLS 之后。

## 部署

### Docker

```bash
docker build -t xiaoyurepeater:0.2.0 .
docker run -d --name repeater \
  -p 8080:8080 \
  -p 50000-50100:50000-50100/udp \
  -v repeater-data:/data \
  -e XR_ADMIN_PASSWORD='<至少 8 位的管理密码>' \
  -e XR_WEBRTC_PUBLIC_IP='<主机对外 IP>' \
  xiaoyurepeater:0.2.0
```

镜像内以非 root 用户运行，数据目录为 `/data`。控制台已嵌入二进制。UDP 端口范围必须与 `XR_WEBRTC_UDP_PORT_MIN/MAX` 一致，且范围大小需覆盖预期的并发媒体连接数；不使用 WebRTC 时可移除 UDP 映射并设置 `XR_WEBRTC_ENABLED=false`。

### 反向代理

Nginx/Caddy 需转发 WebSocket Upgrade 头到本服务；若代理终止 TLS，设置 `XR_TRUST_PROXY=true`，中继会依据 `X-Forwarded-Proto` 生成 `wss://` 加入地址并启用 Secure Cookie。

WebRTC 媒体流量走 UDP，不经过 HTTP 反向代理；需要在主机或云安全组直接放行 `XR_WEBRTC_UDP_PORT_MIN`–`XR_WEBRTC_UDP_PORT_MAX`，并在 NAT 场景配置 `XR_WEBRTC_PUBLIC_IP`。

### 单实例约束

房间与连接状态在进程内维护，当前版本按单实例设计，不支持多副本水平扩展；请勿让多个实例共享同一 `XR_DATA_DIR`。

## 项目结构

```text
.
├── main.go                 启动、TLS、优雅退出
├── internal/
│   ├── auth/               管理会话、密码、登录限速
│   ├── config/             环境变量配置
│   ├── hub/                房间、使用方通道、WS 帧转发、WebRTC SFU
│   ├── password/           PBKDF2 密码哈希
│   ├── protocol/           使用方控制协议与 WebRTC 信令消息
│   ├── server/             HTTP 路由、管理 API、使用方 API、SPA 托管
│   ├── store/              state.json 持久化
│   └── version/            版本号（可由 ldflags 注入）
├── examples/consumer/      使用方示例：创建房间 + 实时鉴权 + 重连
├── sdk/                    可下载 SDK：嵌入归档、生成器、Go/Node.js/Python/curl 示例
├── web/                    Vue 3 + TypeScript 控制台
├── docs/protocol.md        协议与错误码
├── Dockerfile
└── Makefile
```

## 常用命令

```bash
make build        # 构建控制台 + 服务端二进制
make test         # Go 测试
make test-race    # 竞态检测
make docker       # 构建镜像
go generate ./sdk # 修改 sdk/files/ 后重新生成嵌入归档
```

## 许可证

本项目基于 [MIT 许可证](LICENSE) 开源。

Copyright (c) 2026 重庆沛雨霖科技有限公司（PYLinTech）
