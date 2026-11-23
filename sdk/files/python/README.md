# Python SDK v1

Python 3.10+，使用 `websockets` 异步客户端。

## 安装

```sh
cd python
python3 -m venv .venv
. .venv/bin/activate
pip install -r requirements.txt
```

`config.json` 在上一级目录。也可以设置：

```sh
export XR_SERVER="https://relay.example.com"
export XR_API_KEY="你的 API Key"
```

## 快速开始

```python
import asyncio

from xiaoyurepeater_sdk_v1 import Client, with_query


async def main() -> None:
    client = Client()  # 参数：api_key、base_url
    created = await client.create_room(
        name="demo",
        max_participants=20,
        metadata={"tenant": "acme"},
    )
    room = created["room"]
    current = await client.get_room(room["id"])

    print(with_query(created["ws_url"], {"token": "短期令牌", "name": "Alice"}))

    async def authorize(request, _agent):
        token = (request.get("query") or {}).get("token", [""])[0]
        if not await check_token(token):
            return {"allow": False, "reason": "令牌无效"}
        name = (request.get("query") or {}).get("name", ["用户"])[0]
        return {"allow": True, "subject": name}

    def on_hello(hello, _agent):
        print("在线房间", len(hello.get("rooms") or []))

    def on_disconnect(error, retry_in):
        print("通道断开", error, retry_in)

    agent = client.create_agent({
        "authorize": authorize,
        "on_hello": on_hello,
        "on_disconnect": on_disconnect,
    })

    try:
        await agent.run()
    finally:
        await client.close_room(room["id"])


asyncio.run(main())
```

完整示例：

```sh
cd python
python3 -m venv .venv
. .venv/bin/activate
pip install -r requirements.txt
python consumer.py --name "demo room"
```

示例参数：`--server`、`--key`、`--name`、`--token`、`--max`。`--server`、`--key` 为空时回退到 `XR_SERVER` / `config.json` 和 `XR_API_KEY`；`--name`、`--token`、`--max` 分别默认 `XiaoyuRepeater demo room`、`demo-token`、`0`。

## API

### 模块函数

| 名称 | 说明 |
| --- | --- |
| `sdk_version(base_url="")` | 异步公开查询 SDK 元数据，无需密钥 |
| `with_query(raw_url, params)` | 给 `ws://` 或 `wss://` 地址追加查询参数（同名参数覆盖） |
| `agent_url(base_url="")` | 使用方通道地址 |
| `Client` | 控制面客户端 |
| `AgentReplacedError` | 4001：被新连接替换 |
| `KeyRevokedError` | 4003：密钥被撤销 |
| `DisconnectedError` | 通道未连接或已断开 |
| `APIError` | HTTP 或握手失败，含 `status`、`code`、`detail` |

### Client

| 方法 | 说明 |
| --- | --- |
| `Client(api_key="", base_url="")` | API Key 为显式参数 > `XR_API_KEY`（均缺失时报错）；服务地址为显式参数 > `XR_SERVER` > SDK 根目录 `config.json` |
| `await client.sdk_version()` | 查询 SDK 元数据 |
| `await client.create_room(name="", max_participants=0, metadata=None)` | 创建房间；`metadata` 为任意 JSON，中继原样保存 |
| `await client.list_rooms()` | 列出房间 |
| `await client.get_room(room_id)` | 查询单个房间 |
| `await client.close_room(room_id)` | 关闭房间；404 视为成功 |
| `client.agent_url()` | 使用方通道地址 |
| `client.create_agent(handler)` | 创建使用方通道 |

### Agent

| 成员 | 说明 |
| --- | --- |
| `await agent.run()` | 连接并自动重连；调用 `close()` 后返回，终止错误抛出 |
| `await agent.close()` | 停止重连并关闭通道 |
| `agent.connected` | 当前是否在线 |
| `agent.hello` | 最近一次 `hello` 快照 |
| `await agent.subscribe(room_id, conn_id, track_ids=None, all=False)` | 替换订阅并等待确认；等待期间断开时抛出 `DisconnectedError` |

`handler` 可以是对象，也可以是字段名到函数的字典。同步函数和异步函数都支持。

| 字段 | 说明 |
| --- | --- |
| `authorize(request, agent)` | 返回 `{ "allow": bool, "subject"?: str, "reason"?: str }` 或布尔值 |
| `on_hello(hello, agent)` | 连接或重连后的全量快照 |
| `on_user_joined` / `on_user_left` | 用户加入 / 离开 |
| `on_room_closed` | 房间关闭 |
| `on_track_published` / `on_track_unpublished` | 轨道发布 / 结束 |
| `on_subscriptions_updated` | 订阅确认 |
| `on_error` | 通道命令错误 |
| `on_disconnect(error, retry_in)` | 断开通知；`retry_in == 0` 表示终止 |

事件回调由同一把异步锁串行执行；`authorize` 并发执行。回调中可以直接 `await agent.subscribe(...)`。

`subscribe` 会等待 `subscriptions_updated` 或 `error`；等待期间通道断开时抛出 `DisconnectedError`。`all=True` 表示全部当前及未来轨道；空列表表示不订阅任何轨道。SDK 自动维护单调 `revision`，确认中的 `applied`/`pending` 反映实际生效状态；遇到 `stale` 会带新版本自动重试一次。

## 重连与错误

- 普通断开后自动重连：初始等待 1 秒；未收到 `hello` 的连续失败按 2 倍退避，最长 30 秒；收到 `hello` 后重置。重连成功会再次触发 `on_hello`。
- `AgentReplacedError`、`KeyRevokedError`、HTTP 401 终止重连。
- `run()` 在 `close()` 后正常返回；终止错误从 `run()` 抛出。
- 服务端 JSON 字段保持 snake_case，SDK 方法参数使用 Python 风格。

## 验证

```sh
python3 -m py_compile xiaoyurepeater_sdk_v1.py consumer.py
```
