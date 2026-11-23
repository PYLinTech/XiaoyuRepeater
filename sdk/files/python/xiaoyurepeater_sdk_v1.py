"""XiaoyuRepeater Python SDK v1.

User payload and RTP media never pass through this SDK. It manages rooms and
the consumer control channel only.
"""

from __future__ import annotations

import asyncio
import inspect
import json
import os
import secrets
import time
from collections.abc import Awaitable, Callable, Mapping
from pathlib import Path
from typing import Any
from urllib.error import HTTPError, URLError
from urllib.parse import parse_qsl, quote, urlencode, urlsplit, urlunsplit
from urllib.request import Request, urlopen

import websockets
from websockets.exceptions import ConnectionClosed, InvalidHandshake, InvalidStatus

__all__ = [
    "APIError",
    "Agent",
    "AgentReplacedError",
    "Client",
    "DisconnectedError",
    "KeyRevokedError",
    "agent_url",
    "sdk_version",
    "with_query",
]

VERSION = "1"
PROTOCOL_VERSION = 1
_CONFIG_PATH = Path(__file__).resolve().parent.parent / "config.json"
_RETRY_INITIAL = 1.0
_RETRY_MAX = 30.0


class APIError(Exception):
    """HTTP or handshake failure returned by the relay."""

    def __init__(self, status: int, message: str, code: str = "", detail: str = "") -> None:
        super().__init__(message)
        self.status = status
        self.code = code
        self.detail = detail


class DisconnectedError(Exception):
    """The agent channel is not connected."""


class AgentReplacedError(DisconnectedError):
    """Another connection replaced this agent channel."""


class KeyRevokedError(DisconnectedError):
    """The API key was revoked and cannot reconnect."""


def _load_config_base_url() -> str:
    try:
        data = json.loads(_CONFIG_PATH.read_text(encoding="utf-8"))
    except OSError as exc:
        raise ValueError(f"读取 config.json 失败：{exc}") from exc
    except json.JSONDecodeError as exc:
        raise ValueError(f"config.json 不是有效 JSON：{exc}") from exc
    base_url = data.get("baseUrl") if isinstance(data, dict) else None
    if not base_url:
        raise ValueError("config.json 缺少 baseUrl")
    return str(base_url)


def _resolve_base_url(base_url: str = "") -> str:
    raw = (base_url or os.environ.get("XR_SERVER", "") or _load_config_base_url()).rstrip("/")
    parts = urlsplit(raw)
    if parts.scheme not in ("http", "https") or not parts.netloc:
        raise ValueError("服务地址必须以 http:// 或 https:// 开头")
    return raw


def _ws_url(base_url: str, endpoint: str) -> str:
    parts = urlsplit(base_url)
    scheme = "wss" if parts.scheme == "https" else "ws"
    return urlunsplit((scheme, parts.netloc, parts.path.rstrip("/") + endpoint, "", ""))


def agent_url(base_url: str = "") -> str:
    """Return the agent WebSocket URL for a relay base URL."""
    return _ws_url(_resolve_base_url(base_url), "/v1/agent")


def with_query(raw_url: str, params: Mapping[str, Any] | None = None) -> str:
    """Append user join parameters to a ws:// or wss:// room URL."""
    parts = urlsplit(raw_url)
    if parts.scheme not in ("ws", "wss") or not parts.netloc:
        raise ValueError("房间地址必须以 ws:// 或 wss:// 开头")
    params = params or {}
    pairs = [(key, value) for key, value in parse_qsl(parts.query, keep_blank_values=True) if key not in params]
    for key, value in params.items():
        values = value if isinstance(value, (list, tuple)) else [value]
        for item in values:
            pairs.append((str(key), str(item)))
    return urlunsplit((parts.scheme, parts.netloc, parts.path, urlencode(pairs), ""))


def _try_json(text: str) -> Any:
    if not text:
        return {}
    try:
        return json.loads(text)
    except json.JSONDecodeError:
        return None


def _request_sync(
    method: str,
    base_url: str,
    path: str,
    api_key: str = "",
    body: dict[str, Any] | None = None,
) -> dict[str, Any]:
    headers = {
        "Accept": "application/json",
        "User-Agent": "XiaoyuRepeater-SDK/1",
    }
    if api_key:
        headers["Authorization"] = f"Bearer {api_key}"
    data = None
    if body is not None:
        headers["Content-Type"] = "application/json"
        data = json.dumps(body, ensure_ascii=False, separators=(",", ":")).encode("utf-8")

    request = Request(base_url + path, data=data, headers=headers, method=method)
    try:
        with urlopen(request, timeout=15) as response:
            text = response.read().decode("utf-8", errors="replace")
    except HTTPError as exc:
        text = exc.read().decode("utf-8", errors="replace")
        payload = _try_json(text)
        message = payload.get("error") if isinstance(payload, dict) else ""
        code = payload.get("code", "") if isinstance(payload, dict) else ""
        raise APIError(
            exc.code,
            message or f"请求失败（HTTP {exc.code}）",
            code,
            "" if message else text[:300],
        ) from None
    except URLError as exc:
        raise APIError(0, "无法连接服务器", "", str(exc.reason)) from None

    payload = _try_json(text)
    if not isinstance(payload, dict):
        raise APIError(0, "服务端响应不是有效 JSON", "", text[:300])
    return payload


async def sdk_version(base_url: str = "") -> dict[str, Any]:
    """Query the public SDK metadata endpoint without an API key."""
    resolved = _resolve_base_url(base_url)
    return await asyncio.to_thread(_request_sync, "GET", resolved, "/api/sdk/version")


class Client:
    """Control-plane client for creating, reading, listing and closing rooms."""

    def __init__(self, api_key: str = "", base_url: str = "") -> None:
        self.api_key = api_key or os.environ.get("XR_API_KEY", "")
        if not self.api_key:
            raise ValueError("缺少 API Key：设置 XR_API_KEY 或传入 api_key")
        self.base_url = _resolve_base_url(base_url)

    async def sdk_version(self) -> dict[str, Any]:
        return await sdk_version(self.base_url)

    async def create_room(
        self,
        name: str = "",
        max_participants: int = 0,
        metadata: Any = None,
    ) -> dict[str, Any]:
        body: dict[str, Any] = {}
        if name:
            body["name"] = name
        if max_participants:
            body["max_participants"] = int(max_participants)
        if metadata is not None:
            body["metadata"] = metadata
        return await asyncio.to_thread(
            _request_sync, "POST", self.base_url, "/v1/rooms", self.api_key, body
        )

    async def list_rooms(self) -> list[dict[str, Any]]:
        payload = await asyncio.to_thread(
            _request_sync, "GET", self.base_url, "/v1/rooms", self.api_key
        )
        rooms = payload.get("rooms", [])
        return rooms if isinstance(rooms, list) else []

    async def get_room(self, room_id: str) -> dict[str, Any]:
        payload = await asyncio.to_thread(
            _request_sync,
            "GET",
            self.base_url,
            f"/v1/rooms/{quote(room_id, safe='')}",
            self.api_key,
        )
        room = payload.get("room", {})
        return room if isinstance(room, dict) else {}

    async def close_room(self, room_id: str) -> dict[str, Any]:
        try:
            return await asyncio.to_thread(
                _request_sync,
                "DELETE",
                self.base_url,
                f"/v1/rooms/{quote(room_id, safe='')}",
                self.api_key,
            )
        except APIError as exc:
            if exc.status == 404:
                return {"ok": True}
            raise

    def agent_url(self) -> str:
        return _ws_url(self.base_url, "/v1/agent")

    def create_agent(self, handler: Any = None) -> "Agent":
        return Agent(self, handler)


class Agent:
    """One auto-reconnecting consumer control channel.

    Supported handler callbacks: on_hello, authorize, on_user_joined,
    on_user_left, on_room_closed, on_track_published, on_track_unpublished,
    on_subscriptions_updated, on_error and on_disconnect. Handlers may be sync
    or async. Event callbacks are serialized; authorize calls run concurrently.
    """

    def __init__(self, client: Client, handler: Any = None) -> None:
        self._client = client
        self._handler = handler
        self._closed = False
        self._running = False
        self._stop = asyncio.Event()
        self._ws: Any = None
        self._hello: dict[str, Any] | None = None
        self._pending: dict[str, asyncio.Future[dict[str, Any]]] = {}
        self._revision = int(time.time() * 1000)
        self._tasks: set[asyncio.Task[Any]] = set()
        self._dispatch_lock = asyncio.Lock()

    @property
    def hello(self) -> dict[str, Any] | None:
        return self._hello

    @property
    def connected(self) -> bool:
        ws = self._ws
        return ws is not None and getattr(ws, "close_code", None) is None

    async def run(self) -> None:
        """Keep the agent channel connected until close or a terminal error."""
        if self._running:
            raise RuntimeError("Agent 已在运行")
        self._running = True
        backoff = _RETRY_INITIAL
        try:
            while not self._closed:
                saw_hello = False
                try:
                    saw_hello = await self._run_once()
                    if self._closed:
                        return
                    error: Exception = DisconnectedError()
                except (AgentReplacedError, KeyRevokedError) as exc:
                    if self._closed:
                        return
                    await self._notify_disconnect(exc, 0.0)
                    raise
                except APIError as exc:
                    if self._closed:
                        return
                    if exc.status == 401:
                        await self._notify_disconnect(exc, 0.0)
                        raise
                    saw_hello = bool(getattr(exc, "saw_hello", False))
                    error = exc
                except Exception as exc:
                    if self._closed:
                        return
                    saw_hello = bool(getattr(exc, "saw_hello", False))
                    error = exc
                if saw_hello:
                    backoff = _RETRY_INITIAL
                retry = backoff
                await self._notify_disconnect(error, retry)
                await self._sleep_or_closed(retry)
                if not saw_hello:
                    backoff = min(backoff * 2, _RETRY_MAX)
        finally:
            self._running = False

    async def close(self) -> None:
        """Stop reconnecting and close the current agent channel."""
        self._closed = True
        self._stop.set()
        self._reject_pending(DisconnectedError("使用方通道已关闭"))
        current = asyncio.current_task()
        for task in list(self._tasks):
            if task is not current:
                task.cancel()
        self._tasks.clear()
        ws = self._ws
        if ws is not None:
            try:
                await ws.close()
            except Exception:
                pass

    async def subscribe(
        self,
        room_id: str,
        conn_id: str,
        track_ids: list[str] | None = None,
        all: bool = False,
    ) -> dict[str, Any]:
        """Replace one WebRTC participant's complete subscription list."""
        if not room_id or not conn_id:
            raise ValueError("room_id 与 conn_id 不能为空")
        # 服务端可能保留更高版本（例如使用方进程刚重启），陈旧命令未生效时带新版本重试一次
        retry = True
        while True:
            if not self.connected:
                raise DisconnectedError("使用方通道未连接")
            request_id = f"sub_{secrets.token_hex(8)}"
            future: asyncio.Future[dict[str, Any]] = asyncio.get_running_loop().create_future()
            self._pending[request_id] = future
            self._revision += 1
            try:
                await self._send(
                    {
                        "type": "subscriptions",
                        "request_id": request_id,
                        "room_id": room_id,
                        "conn_id": conn_id,
                        "track_ids": list(track_ids or []),
                        "all": bool(all),
                        "revision": self._revision,
                    }
                )
            except Exception:
                self._pending.pop(request_id, None)
                raise
            try:
                update = await future
            finally:
                self._pending.pop(request_id, None)
            revision = update.get("revision")
            if isinstance(revision, int) and revision > self._revision:
                self._revision = revision
            if not update.get("stale") or not retry:
                return update
            retry = False

    async def _run_once(self) -> bool:
        try:
            ws = await websockets.connect(
                self._client.agent_url(),
                additional_headers={"Authorization": f"Bearer {self._client.api_key}"},
                open_timeout=10,
                close_timeout=3,
                ping_interval=20,
                ping_timeout=20,
                max_size=1 << 20,
                max_queue=128,
            )
        except InvalidStatus as exc:
            status = getattr(exc, "status_code", 0)
            if not status:
                status = getattr(getattr(exc, "response", None), "status_code", 0)
            raise APIError(status, f"连接使用方通道失败（HTTP {status}）") from None
        except (InvalidHandshake, OSError, asyncio.TimeoutError, TimeoutError) as exc:
            raise DisconnectedError(f"无法连接使用方通道：{exc}") from None

        self._ws = ws
        if self._closed:
            await ws.close()
            self._ws = None
            return False
        saw_hello = False
        try:
            while True:
                raw = await ws.recv()
                if isinstance(raw, bytes):
                    try:
                        raw = raw.decode("utf-8")
                    except UnicodeDecodeError:
                        continue
                try:
                    message = json.loads(raw)
                except json.JSONDecodeError:
                    continue
                if not isinstance(message, dict):
                    continue
                if message.get("type") == "hello":
                    saw_hello = True
                self._handle_message(message)
        except ConnectionClosed as exc:
            if exc.code == 4001:
                raise AgentReplacedError() from None
            if exc.code == 4003:
                raise KeyRevokedError() from None
            return saw_hello
        except Exception as exc:
            try:
                setattr(exc, "saw_hello", saw_hello)
            except Exception:
                pass
            raise
        finally:
            self._ws = None
            self._reject_pending(DisconnectedError())

    def _handle_message(self, message: dict[str, Any]) -> None:
        kind = message.get("type")
        if kind == "hello":
            self._hello = message
            self._spawn(self._dispatch("on_hello", message))
        elif kind == "auth_request":
            if message.get("request_id"):
                self._spawn(self._authorize(message))
        elif kind == "user_joined":
            self._spawn(self._dispatch("on_user_joined", message))
        elif kind == "user_left":
            self._spawn(self._dispatch("on_user_left", message))
        elif kind == "room_closed":
            self._spawn(self._dispatch("on_room_closed", message))
        elif kind == "track_published":
            self._spawn(self._dispatch("on_track_published", message))
        elif kind == "track_unpublished":
            self._spawn(self._dispatch("on_track_unpublished", message))
        elif kind == "subscriptions_updated":
            self._resolve_pending(message)
            self._spawn(self._dispatch("on_subscriptions_updated", message))
        elif kind == "error":
            self._reject_one(message)
            self._spawn(self._dispatch("on_error", message))
        elif kind == "ping":
            self._spawn(self._pong())

    async def _authorize(self, request: dict[str, Any]) -> None:
        decision: Any = {"allow": False, "reason": "使用方未配置鉴权处理器"}
        callback = self._handler_for("authorize")
        if callback is not None:
            try:
                decision = callback(request, self)
                if inspect.isawaitable(decision):
                    decision = await decision
            except Exception:
                decision = {"allow": False, "reason": "鉴权处理器异常"}
        if isinstance(decision, bool):
            decision = {"allow": decision}
        allow = bool(decision.get("allow")) if isinstance(decision, Mapping) else False
        response: dict[str, Any] = {
            "type": "auth_response",
            "request_id": request.get("request_id", ""),
            "allow": allow,
        }
        if isinstance(decision, Mapping):
            if decision.get("subject"):
                response["subject"] = str(decision["subject"])
            if decision.get("reason"):
                response["reason"] = str(decision["reason"])
        try:
            await self._send(response)
        except Exception:
            pass

    async def _dispatch(self, name: str, event: dict[str, Any]) -> None:
        callback = self._handler_for(name)
        if callback is None:
            return
        async with self._dispatch_lock:
            try:
                result = callback(event, self)
                if inspect.isawaitable(result):
                    await result
            except Exception:
                pass

    async def _pong(self) -> None:
        try:
            await self._send({"type": "pong"})
        except Exception:
            pass

    async def _send(self, payload: dict[str, Any]) -> None:
        ws = self._ws
        if ws is None or not self.connected:
            raise DisconnectedError("使用方通道未连接")
        try:
            await ws.send(json.dumps(payload, ensure_ascii=False, separators=(",", ":")))
        except ConnectionClosed as exc:
            raise DisconnectedError("使用方通道已断开") from exc

    def _handler_for(self, name: str) -> Callable[..., Any] | None:
        handler = self._handler
        if handler is None:
            return None
        if isinstance(handler, Mapping):
            callback = handler.get(name)
        else:
            callback = getattr(handler, name, None)
        return callback if callable(callback) else None

    async def _notify_disconnect(self, error: Exception, retry_in: float) -> None:
        callback = self._handler_for("on_disconnect")
        if callback is None:
            return
        try:
            result = callback(error, retry_in)
            if inspect.isawaitable(result):
                await result
        except Exception:
            pass

    def _spawn(self, coroutine: Awaitable[Any]) -> None:
        task = asyncio.create_task(coroutine)
        self._tasks.add(task)
        task.add_done_callback(self._task_done)

    def _task_done(self, task: asyncio.Task[Any]) -> None:
        self._tasks.discard(task)
        if not task.cancelled():
            try:
                task.exception()
            except Exception:
                pass

    def _resolve_pending(self, message: dict[str, Any]) -> None:
        request_id = message.get("request_id", "")
        future = self._pending.get(request_id)
        if future is not None and not future.done():
            future.set_result(message)

    def _reject_one(self, message: dict[str, Any]) -> None:
        request_id = message.get("request_id", "")
        future = self._pending.get(request_id)
        if future is not None and not future.done():
            future.set_exception(RuntimeError(message.get("error") or "控制通道返回错误"))

    def _reject_pending(self, error: Exception) -> None:
        for future in self._pending.values():
            if not future.done():
                future.set_exception(error)
        self._pending.clear()

    async def _sleep_or_closed(self, seconds: float) -> None:
        try:
            await asyncio.wait_for(self._stop.wait(), timeout=seconds)
        except asyncio.TimeoutError:
            pass
