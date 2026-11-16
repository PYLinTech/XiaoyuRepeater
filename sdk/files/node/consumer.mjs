#!/usr/bin/env node
// Runnable consumer demo for XiaoyuRepeater SDK v1.
// It creates one room, keeps the agent channel open, authorizes users by a
// demo token, and closes the room when the process is stopped.

import process from "node:process";
import { AgentReplacedError, Client, KeyRevokedError, withQuery } from "./xiaoyurepeater-sdk-v1.mjs";

function parseArgs(argv) {
  const args = {};
  for (let index = 0; index < argv.length; index += 1) {
    const [flag, inline] = argv[index].split("=", 2);
    if (!flag.startsWith("-")) continue;
    const name = flag.replace(/^-+/, "");
    if (inline !== undefined) {
      args[name] = inline;
    } else if (argv[index + 1] && !argv[index + 1].startsWith("-")) {
      args[name] = argv[index + 1];
      index += 1;
    } else {
      args[name] = "true";
    }
  }
  return args;
}

function log(message) {
  console.log(`[${new Date().toISOString()}] ${message}`);
}

const args = parseArgs(process.argv.slice(2));
if (!args.key && !process.env.XR_API_KEY) {
  console.error("缺少 API Key：传入 -key 或设置 XR_API_KEY");
  process.exit(2);
}

const demoToken = args.token || "demo-token";
const client = new Client(args.key || "", args.server || "");
const maxParticipants = Number(args.max || 0);
const created = await client.createRoom({
  name: args.name || "XiaoyuRepeater demo room",
  maxParticipants: Number.isFinite(maxParticipants) && maxParticipants > 0 ? maxParticipants : 0,
});
const room = created.room;
const joinParams = { token: demoToken, name: "demo-user" };

log(`房间已创建：${room.id}（${room.name}）`);
log(`WS 加入地址：${withQuery(created.ws_url, joinParams)}`);
if (created.webrtc_url) {
  log(`WebRTC 加入地址：${withQuery(created.webrtc_url, joinParams)}`);
}
log("保持使用方通道在线，按 Ctrl+C 结束。");

const agent = client.createAgent({
  authorize: async (request) => {
    const token = request.query?.token?.[0] || "";
    if (token !== demoToken) {
      return { allow: false, reason: "token 不正确" };
    }
    return {
      allow: true,
      subject: request.query?.name?.[0] || "用户",
    };
  },
  onHello: (hello) => {
    log(`使用方通道就绪：agent=${hello.agent_id}，房间数=${hello.rooms?.length || 0}`);
  },
  onUserJoined: (event) => {
    log(`用户加入：${event.conn_id}，transport=${event.transport}，subject=${event.subject}`);
  },
  onUserLeft: (event) => {
    log(`用户离开：${event.conn_id}，reason=${event.reason}`);
  },
  onRoomClosed: (event) => {
    log(`房间已关闭：${event.room_id}，reason=${event.reason}`);
  },
  onTrackPublished: (event) => {
    log(`新增轨道：${event.track?.id}，kind=${event.track?.kind}`);
  },
  onTrackUnpublished: (event) => {
    log(`移除轨道：${event.track?.id}`);
  },
  onError: (event) => {
    log(`控制通道错误：${event.error}`);
  },
  onDisconnect: (error, retryInMs) => {
    const detail = retryInMs > 0 ? `，${retryInMs} ms 后重连` : "";
    log(`使用方通道断开：${error.message}${detail}`);
  },
});

let interrupted = false;
const stop = () => {
  interrupted = true;
  log("正在关闭房间并退出。");
  agent.close();
};
process.once("SIGINT", stop);
process.once("SIGTERM", stop);

let exitCode = 0;
try {
  await agent.run();
} catch (error) {
  if (error instanceof AgentReplacedError || error instanceof KeyRevokedError) {
    log(`使用方通道终止：${error.message}`);
  } else {
    log(`使用方通道失败：${error.message}`);
  }
  exitCode = 1;
}

try {
  await client.closeRoom(room.id);
  log("房间已关闭。");
} catch (error) {
  log(`关闭房间失败：${error.message}`);
  if (!interrupted) exitCode = 1;
}
process.exit(exitCode);
