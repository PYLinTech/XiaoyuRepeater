#!/usr/bin/env python3
"""Runnable XiaoyuRepeater consumer demo.

It creates one room, authorizes users by a demo token, keeps the control
channel online, and closes the room when stopped.
"""

from __future__ import annotations

import argparse
import asyncio
import os
import signal
from datetime import datetime, timezone

from xiaoyurepeater_sdk_v1 import (
    AgentReplacedError,
    Client,
    KeyRevokedError,
    with_query,
)


def log(message: str) -> None:
    print(f"[{datetime.now(timezone.utc).isoformat()}] {message}", flush=True)


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description="XiaoyuRepeater SDK v1 consumer demo")
    parser.add_argument("--server", default="", help="中继服务地址，默认读取 config.json 或 XR_SERVER")
    parser.add_argument("--key", default="", help="API Key，默认读取 XR_API_KEY")
    parser.add_argument("--name", default="XiaoyuRepeater demo room", help="房间名称")
    parser.add_argument("--token", default="demo-token", help="演示用用户令牌")
    parser.add_argument("--max", type=int, default=0, help="房间最大参与人数")
    return parser


async def main() -> int:
    args = build_parser().parse_args()
    if not args.key and not os.environ.get("XR_API_KEY"):
        print("缺少 API Key：传入 --key 或设置 XR_API_KEY", flush=True)
        return 2

    client = Client(args.key, args.server)
    created = await client.create_room(
        name=args.name,
        max_participants=args.max if args.max > 0 else 0,
    )
    room = created["room"]
    join_params = {"token": args.token, "name": "demo-user"}
    log(f"房间已创建：{room['id']}（{room['name']}）")
    log(f"WS 加入地址：{with_query(created['ws_url'], join_params)}")
    if created.get("webrtc_url"):
        log(f"WebRTC 加入地址：{with_query(created['webrtc_url'], join_params)}")
    log("保持使用方通道在线，按 Ctrl+C 结束。")

    def authorize(request, _agent):
        token = (request.get("query") or {}).get("token", [""])[0]
        if token != args.token:
            return {"allow": False, "reason": "token 不正确"}
        name = (request.get("query") or {}).get("name", ["用户"])[0]
        return {"allow": True, "subject": name or "用户"}

    def on_disconnect(error, retry_in):
        suffix = f"，{retry_in:.1f} 秒后重连" if retry_in > 0 else ""
        log(f"使用方通道断开：{error}{suffix}")

    agent = client.create_agent(
        {
            "authorize": authorize,
            "on_hello": lambda hello, _agent: log(
                f"使用方通道就绪：agent={hello.get('agent_id')}，"
                f"房间数={len(hello.get('rooms') or [])}"
            ),
            "on_user_joined": lambda event, _agent: log(
                f"用户加入：{event.get('conn_id')}，transport={event.get('transport')}，"
                f"subject={event.get('subject')}"
            ),
            "on_user_left": lambda event, _agent: log(
                f"用户离开：{event.get('conn_id')}，reason={event.get('reason')}"
            ),
            "on_room_closed": lambda event, _agent: log(
                f"房间已关闭：{event.get('room_id')}，reason={event.get('reason')}"
            ),
            "on_track_published": lambda event, _agent: log(
                f"新增轨道：{(event.get('track') or {}).get('id')}"
            ),
            "on_track_unpublished": lambda event, _agent: log(
                f"移除轨道：{(event.get('track') or {}).get('id')}"
            ),
            "on_error": lambda event, _agent: log(f"控制通道错误：{event.get('error')}"),
            "on_disconnect": on_disconnect,
        }
    )

    loop = asyncio.get_running_loop()
    stop_requested = False

    def request_stop() -> None:
        nonlocal stop_requested
        stop_requested = True
        log("正在关闭房间并退出。")
        asyncio.create_task(agent.close())

    try:
        loop.add_signal_handler(signal.SIGINT, request_stop)
        loop.add_signal_handler(signal.SIGTERM, request_stop)
    except NotImplementedError:
        pass

    exit_code = 0
    try:
        await agent.run()
    except (AgentReplacedError, KeyRevokedError) as exc:
        log(f"使用方通道终止：{exc}")
        exit_code = 1
    except Exception as exc:
        log(f"使用方通道失败：{exc}")
        exit_code = 1

    try:
        await client.close_room(room["id"])
        log("房间已关闭。")
    except Exception as exc:
        log(f"关闭房间失败：{exc}")
        if not stop_requested:
            exit_code = 1
    return exit_code


if __name__ == "__main__":
    raise SystemExit(asyncio.run(main()))
