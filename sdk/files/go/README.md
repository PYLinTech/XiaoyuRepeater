# Go SDK v1

Go 1.24+。包名 `xiaoyurepeater`，覆盖房间管理、使用方通道、鉴权回调和轨道订阅。

## 引入

SDK 以独立模块放在 `go/`。在业务项目的 `go.mod` 中添加本地替换（假设 SDK 解压到 `XiaoyuRepeater_SDK_v1/`，请按实际解压位置调整路径）：

```go
require gitee.com/PYLinTech/XiaoyuRepeater/sdk/go v0.0.0

replace gitee.com/PYLinTech/XiaoyuRepeater/sdk/go => ../XiaoyuRepeater_SDK_v1/go
```

导入：

```go
import xiaoyurepeater "gitee.com/PYLinTech/XiaoyuRepeater/sdk/go"
```

如果使用 GOPATH 或复制源码，也可以直接把 `go/` 下的包文件放进项目。除 `github.com/gorilla/websocket` 外没有其他依赖。

## 配置

```sh
export XR_SERVER="https://relay.example.com"
export XR_API_KEY="你的 API Key"
```

`NewClient("", "")` 的查找顺序：API Key 为显式参数 > `XR_API_KEY`（均缺失时报错）；服务地址为显式参数 > `XR_SERVER` > `config.json`。`config.json` 依次在包目录上一级、当前工作目录及其上级目录中查找。生产代码建议显式传入或只用环境变量，不依赖工作目录。

## 快速开始

```go
package main

import (
	"context"
	"log"
	"net/url"
	"time"

	xiaoyurepeater "gitee.com/PYLinTech/XiaoyuRepeater/sdk/go"
)

func main() {
	ctx := context.Background()

	client, err := xiaoyurepeater.NewClient("", "")
	if err != nil {
		log.Fatal(err)
	}

	created, err := client.CreateRoom(ctx, xiaoyurepeater.RoomOptions{
		Name:            "demo",
		MaxParticipants: 20,
	})
	if err != nil {
		log.Fatal(err)
	}

	joinURL, err := created.JoinWSURL(url.Values{
		"token": {"短期令牌"},
		"name":  {"Alice"},
	})
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("用户加入地址：%s", joinURL)

	agent := client.NewAgent(xiaoyurepeater.AgentHandler{
		Authorize: func(ctx context.Context, _ *xiaoyurepeater.Agent, request xiaoyurepeater.AuthRequest) xiaoyurepeater.AuthDecision {
			if first(request.Query["token"]) != "短期令牌" {
				return xiaoyurepeater.Deny("令牌无效")
			}
			return xiaoyurepeater.Allow(first(request.Query["name"]))
		},
		OnUserJoined: func(_ context.Context, _ *xiaoyurepeater.Agent, event xiaoyurepeater.UserJoined) {
			log.Printf("%s 加入 %s", event.Subject, event.RoomName)
		},
		OnDisconnect: func(err error, retryIn time.Duration) {
			if retryIn > 0 {
				log.Printf("通道断开：%v，%s 后重连", err, retryIn)
			} else {
				log.Printf("通道断开：%v", err)
			}
		},
	})

	if err := agent.Run(ctx); err != nil {
		log.Fatal(err)
	}
}

func first(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[0]
}
```

完整可运行示例见 `cmd/consumer/`：

```sh
cd go
go run ./cmd/consumer -name "demo room"
```

示例支持 `-server`、`-key`、`-name`、`-token`、`-max` 参数。`-server`、`-key` 为空时回退到 `XR_SERVER` / `config.json` 和 `XR_API_KEY`；`-name`、`-token`、`-max` 分别默认 `演示房间`、`demo-token`、`0`（0 表示使用中继默认人数上限）。

## API

### Client

| 方法 | 说明 |
| --- | --- |
| `NewClient(apiKey, baseURL string) (*Client, error)` | 创建客户端 |
| `SDKVersion(ctx, baseURL string) (VersionInfo, error)` | 公开查询 SDK 元数据，无需密钥 |
| `(*Client).SDKVersion(ctx)` | 查询当前中继的 SDK 元数据 |
| `CreateRoom(ctx, RoomOptions) (CreatedRoom, error)` | 创建房间 |
| `ListRooms(ctx) ([]Room, error)` | 列出当前密钥的房间 |
| `CloseRoom(ctx, roomID string) error` | 关闭房间；404（不存在或已关闭）视为成功 |
| `AgentURL() (string, error)` | 使用方通道地址 |
| `CreatedRoom.JoinWSURL(url.Values)` | 生成用户 WS 加入地址 |
| `CreatedRoom.JoinWebRTCURL(url.Values)` | 生成用户 WebRTC 信令地址 |
| `JoinURL(rawURL string, url.Values)` | 给任意房间地址追加查询参数（同名参数覆盖） |

`RoomOptions` 的零值会被省略，由中继应用自身默认配置。

### Agent

| 方法 | 说明 |
| --- | --- |
| `Client.NewAgent(AgentHandler) *Agent` | 创建使用方通道 |
| `Agent.Run(ctx) error` | 连接并阻塞，自动重连，直到取消或终止错误 |
| `Agent.Close() error` | 停止重连并关闭通道 |
| `Agent.Connected() bool` | 当前是否在线 |
| `Agent.Hello() (Hello, bool)` | 最近一次全量快照 |
| `Agent.SetSubscriptions(ctx, roomID, connID, trackIDs, all)` | 替换一个 WebRTC 参与者的订阅并等待确认 |

回调字段：

| 字段 | 说明 |
| --- | --- |
| `OnHello` | 连接或重连后的全量快照 |
| `Authorize` | 用户加入鉴权；返回 `Allow(subject)` 或 `Deny(reason)` |
| `OnUserJoined` / `OnUserLeft` | 用户加入 / 离开 |
| `OnRoomClosed` | 房间关闭 |
| `OnTrackPublished` / `OnTrackUnpublished` | 轨道发布 / 结束 |
| `OnSubscriptionsUpdated` | 订阅确认 |
| `OnError` | 通道命令错误 |
| `OnDisconnect` | 通道断开；`retryIn == 0` 表示终止 |

`OnHello` 与事件回调由单个调度协程按顺序执行；`Authorize` 并发执行，慢鉴权不会阻塞读取循环。回调应尽快返回，不要在其中执行长时间阻塞操作。回调里可以调用 `SetSubscriptions`。

`SetSubscriptions` 会等待 `subscriptions_updated` 或 `error`；等待期间通道断开返回 `ErrDisconnected`，`ctx` 结束返回对应错误。`all=true` 表示订阅全部当前及未来轨道；空列表表示不订阅任何轨道。

## 重连与错误

- 普通断开后自动重连：初始等待 1 秒；未收到 `hello` 的连续失败按 2 倍退避，最长 30 秒；收到 `hello` 后重置。重连成功会再次触发 `OnHello`。
- 返回 `ErrAgentReplaced`（4001）、`ErrKeyRevoked`（4003）或 HTTP 401 时停止重连。
- `APIError` 包含 `Status`、`Code`、`Message`、`Detail`。
- 用户加入被拒绝时，用户侧收到 403；使用方通道保持在线。

## 验证

```sh
cd go
gofmt -l .
go build ./...
go vet ./...
```
