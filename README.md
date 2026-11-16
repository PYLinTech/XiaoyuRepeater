# XiaoyuRepeater

面向实时场景的通用 WebSocket 中继服务。使用方程序凭 API 密钥创建中转房间，把房间地址分发给自己的用户；用户连接房间时，中继服务通过双向 WebSocket 实时询问使用方程序是否放行，通过后即可加入。

数据面只做字节转发：文本帧与二进制帧原样复制给房间内其他连接，不解析、不落盘、不转码，也没有任何业务内容规则。内置 Go 服务端与 Vue 控制台，单个二进制即可部署。

## 适用场景

- 会议流分发、直播旁路分发
- 远程协助 / 远程控制信令与画面转发
- 其他需要“房间 + 使用方实时鉴权 + 透明转发”的实时应用

中继不包含编解码、混流、录制、内容审核等能力，具体传输内容完全由使用方与用户自行约定。

## 工作方式

```text
                     控制面（HTTP / WebSocket）
 使用方程序  ── 创建房间、关闭房间 ──────────────┐
     │                                          ▼
     │  WS /v1/agent（鉴权通道）          ┌──────────────┐
     └──── auth_request / auth_response ─│  XiaoyuRepeater │
                                         │   Go 服务端     │
 用户 A ── WS /v1/rooms/{id}/ws ────────▶│  房间内字节转发  │
 用户 B ── WS /v1/rooms/{id}/ws ────────▶│                │
                                         └──────────────┘
                                                ▲
 管理员 ── 浏览器控制台（Vue）──────────────────┘
```

- **控制面**：API 密钥、房间生命周期、加入前鉴权、事件通知。
- **数据面**：用户与用户之间的 WebSocket 帧转发；使用方程序只参与鉴权，不接收转发内容。
- **鉴权在升级前完成**：未通过时直接返回 HTTP 403/409/503/504，不会建立用户连接。

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
| `XR_LOG_LEVEL` | `info` | `debug` / `info` / `warn` / `error` |

## HTTP / WebSocket 接口

使用方接口使用 `Authorization: Bearer <API密钥>`（也接受 `X-Api-Key`）。

| 方法 | 路径 | 用途 |
| --- | --- | --- |
| `POST` | `/v1/rooms` | 创建房间，返回 `ws_path` / `ws_url` |
| `GET` | `/v1/rooms` | 列出当前密钥名下的房间 |
| `DELETE` | `/v1/rooms/{id}` | 关闭自己的房间 |
| `WS` | `/v1/agent` | 使用方鉴权通道（每个密钥同时一个连接） |
| `WS` | `/v1/rooms/{id}/ws` | 用户加入房间 |
| `GET` | `/healthz` | 健康检查 |

管理接口位于 `/api/admin/*`，使用 HttpOnly 会话 Cookie；完整的控制协议、鉴权消息、错误码与转发语义见 [docs/protocol.md](docs/protocol.md)。

## 转发语义

- 只接受 WebSocket 文本帧与二进制帧；消息类型保持不变，原样发给房间内除发送者以外的所有连接。
- 中继不解析帧内容、不区分业务类型、不记录帧内容；只统计消息条数与字节数。
- 每个用户连接有独立发送队列。队列写满表示接收端过慢，该连接会被关闭（1008），不影响其他用户。
- 使用方程序不接收转发数据，只收到加入/离开/房间关闭等控制事件。
- 房间与在线连接保存在内存中，服务重启后房间消失；API 密钥与管理密码持久化在 `XR_DATA_DIR/state.json`。

## 安全说明

- API 密钥只保存哈希，明文只在创建时显示一次，日志中不记录密钥与用户令牌。
- 管理密码使用 PBKDF2-SHA256（210,000 次迭代）；登录失败按 IP 限速。
- 管理员会话 Cookie 为 `HttpOnly` + `SameSite=Strict`；HTTPS 下自动加 `Secure`。
- 转发给使用方的用户请求会剔除 Cookie 与 WebSocket 握手头，避免控制台会话泄漏；query 与请求头各有 16KB 预算。
- 完整密钥、管理密码不要写入仓库或镜像；生产环境建议放在反向代理或直连 TLS 之后。

## 部署

### Docker

```bash
docker build -t xiaoyurepeater:0.1.0 .
docker run -d --name repeater \
  -p 8080:8080 \
  -v repeater-data:/data \
  -e XR_ADMIN_PASSWORD='<至少 8 位的管理密码>' \
  xiaoyurepeater:0.1.0
```

镜像内以非 root 用户运行，数据目录为 `/data`。控制台已嵌入二进制。

### 反向代理

Nginx/Caddy 需转发 WebSocket Upgrade 头到本服务；若代理终止 TLS，设置 `XR_TRUST_PROXY=true`，中继会依据 `X-Forwarded-Proto` 生成 `wss://` 加入地址并启用 Secure Cookie。

### 单实例约束

房间与连接状态在进程内维护，当前版本按单实例设计，不支持多副本水平扩展；请勿让多个实例共享同一 `XR_DATA_DIR`。

## 项目结构

```text
.
├── main.go                 启动、TLS、优雅退出
├── internal/
│   ├── auth/               管理会话、密码、登录限速
│   ├── config/             环境变量配置
│   ├── hub/                房间、使用方通道、用户连接与字节转发
│   ├── password/           PBKDF2 密码哈希
│   ├── protocol/           使用方控制协议消息
│   ├── server/             HTTP 路由、管理 API、使用方 API、SPA 托管
│   ├── store/              state.json 持久化
│   └── version/            版本号（可由 ldflags 注入）
├── examples/consumer/      使用方示例：创建房间 + 实时鉴权 + 重连
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
```

## 许可证

本项目基于 [MIT 许可证](LICENSE) 开源。

Copyright (c) 2026 重庆沛雨霖科技有限公司（PYLinTech）
