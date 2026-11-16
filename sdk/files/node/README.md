# Node.js SDK v1

Node.js 18+。ES Module，依赖 `ws` 以在握手阶段发送 `Authorization` 头。

## 安装

```sh
cd node
npm install
```

`config.json` 在上一级目录。也可以设置：

```sh
export XR_SERVER="https://relay.example.com"
export XR_API_KEY="你的 API Key"
```

## 快速开始

```js
import { Client, withQuery } from "./xiaoyurepeater-sdk-v1.mjs";

const client = new Client("", ""); // 参数：apiKey、baseUrl
const created = await client.createRoom({ name: "demo", maxParticipants: 20 });
const room = created.room;

console.log(withQuery(created.ws_url, { token: "短期令牌", name: "Alice" }));

const agent = client.createAgent({
  authorize: async (request) => {
    const token = request.query?.token?.[0] || "";
    if (!(await checkToken(token))) {
      return { allow: false, reason: "令牌无效" };
    }
    return { allow: true, subject: request.query?.name?.[0] || "用户" };
  },
  onHello: (hello, agentInstance) => {
    console.log("在线房间", hello.rooms.length);
  },
  onUserJoined: (event) => {
    console.log(`${event.subject} 加入 ${event.room_name}`);
  },
  onDisconnect: (error, retryInMs) => {
    console.log("通道断开", error.message, retryInMs);
  },
});

await agent.run();
```

完整示例：

```sh
cd node
npm install
node consumer.mjs -name "demo room"
```

示例参数：`-server`、`-key`、`-name`、`-token`、`-max`，也支持 `--flag=value` 形式。`-server`、`-key` 为空时回退到 `XR_SERVER` / `config.json` 和 `XR_API_KEY`；`-name`、`-token`、`-max` 分别默认 `XiaoyuRepeater demo room`、`demo-token`、`0`。

## API

### 模块函数

| 导出 | 说明 |
| --- | --- |
| `sdkVersion(baseUrl?)` | 公开查询 SDK 元数据，无需密钥 |
| `withQuery(rawUrl, params)` | 给 `ws://` 或 `wss://` 地址追加查询参数（同名参数覆盖） |
| `Client` | 控制面客户端 |
| `AgentReplacedError` | 4001：被新连接替换 |
| `KeyRevokedError` | 4003：密钥被撤销 |
| `DisconnectedError` | 通道未连接或已断开 |
| `APIError` | HTTP 或握手失败，含 `status`、`code`、`detail` |

### Client

| 方法 | 说明 |
| --- | --- |
| `new Client(apiKey?, baseUrl?)` | API Key 为显式参数 > `XR_API_KEY`（均缺失时报错）；服务地址为显式参数 > `XR_SERVER` > SDK 根目录 `config.json` |
| `client.sdkVersion()` | 查询 SDK 元数据 |
| `client.createRoom({ name?, maxParticipants? })` | 创建房间 |
| `client.listRooms()` | 列出房间 |
| `client.closeRoom(roomId)` | 关闭房间；404 视为成功 |
| `client.agentUrl()` | 使用方通道地址 |
| `client.createAgent(handler?)` | 创建使用方通道 |

### Agent

| 成员 | 说明 |
| --- | --- |
| `agent.run()` | 连接并自动重连；调用 `close()` 后 resolve，终止错误时 reject |
| `agent.close()` | 停止重连并关闭通道 |
| `agent.connected` | 当前是否在线 |
| `agent.hello` | 最近一次 `hello` 快照 |
| `agent.subscribe(roomId, connId, { trackIds?, all? })` | 替换订阅并等待确认；等待期间断开时以 `DisconnectedError` reject |

处理器字段：

| 字段 | 说明 |
| --- | --- |
| `authorize(request, agent)` | 返回 `{ allow, subject?, reason? }` 或布尔值 |
| `onHello(hello, agent)` | 连接或重连后的全量快照 |
| `onUserJoined` / `onUserLeft` | 用户加入 / 离开 |
| `onRoomClosed` | 房间关闭 |
| `onTrackPublished` / `onTrackUnpublished` | 轨道发布 / 结束 |
| `onSubscriptionsUpdated` | 订阅确认 |
| `onError` | 通道命令错误 |
| `onDisconnect(error, retryInMs)` | 断开通知；`retryInMs === 0` 表示终止 |

事件回调按顺序串行执行；`authorize` 并发执行。回调可以返回 Promise，SDK 不会因为某个事件回调阻塞读取循环。回调内可以调用 `agent.subscribe`。

`subscribe` 会等待 `subscriptions_updated` 或 `error`；等待期间通道断开时以 `DisconnectedError` reject。`all: true` 表示全部当前及未来轨道；空数组表示不订阅任何轨道。

## 重连与错误

- 普通断开后自动重连：初始等待 1 秒；未收到 `hello` 的连续失败按 2 倍退避，最长 30 秒；收到 `hello` 后重置。重连成功会再次触发 `onHello`。
- `AgentReplacedError`、`KeyRevokedError`、HTTP 401 终止重连。
- `run()` 在调用 `close()` 后正常返回；终止错误从 `run()` 抛出。
- 字段名遵循服务端 JSON 的 snake_case；SDK 方法参数使用 camelCase。

## 验证

```sh
node --check xiaoyurepeater-sdk-v1.mjs
node --check consumer.mjs
```
