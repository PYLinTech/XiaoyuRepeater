#!/usr/bin/env node
// XiaoyuRepeater Node.js SDK v1.
// The relay address comes from XR_SERVER or ../config.json; the API key comes
// from XR_API_KEY or an explicit argument. User payload and RTP media never
// pass through this SDK.

import { randomUUID } from "node:crypto";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import WebSocket from "ws";

const CONFIG_PATH = fileURLToPath(new URL("../config.json", import.meta.url));
const RETRY_INITIAL_MS = 1_000;
const RETRY_MAX_MS = 30_000;

export class APIError extends Error {
  constructor(status, message, code = "", detail = "") {
    super(message);
    this.name = "APIError";
    this.status = status;
    this.code = code;
    this.detail = detail;
  }
}

export class DisconnectedError extends Error {
  constructor(message = "使用方通道已断开") {
    super(message);
    this.name = "DisconnectedError";
  }
}

export class AgentReplacedError extends Error {
  constructor() {
    super("使用方通道已被新连接替换");
    this.name = "AgentReplacedError";
  }
}

export class KeyRevokedError extends Error {
  constructor() {
    super("API 密钥已被撤销");
    this.name = "KeyRevokedError";
  }
}

function loadConfigBaseUrl() {
  let parsed;
  try {
    parsed = JSON.parse(readFileSync(CONFIG_PATH, "utf8"));
  } catch (error) {
    throw new Error(`读取 config.json 失败：${error.message}`);
  }
  if (!parsed?.baseUrl) throw new Error("config.json 缺少 baseUrl");
  return String(parsed.baseUrl);
}

function resolveBaseUrl(baseUrl = "") {
  const raw = String(baseUrl || process.env.XR_SERVER || loadConfigBaseUrl()).replace(/\/+$/, "");
  let parsed;
  try {
    parsed = new URL(raw);
  } catch {
    throw new Error("服务地址必须以 http:// 或 https:// 开头");
  }
  if (!["http:", "https:"].includes(parsed.protocol) || !parsed.host) {
    throw new Error("服务地址必须以 http:// 或 https:// 开头");
  }
  return raw;
}

function webSocketUrl(baseUrl, endpoint) {
  const parsed = new URL(baseUrl);
  parsed.protocol = parsed.protocol === "https:" ? "wss:" : "ws:";
  parsed.pathname = `${parsed.pathname.replace(/\/+$/, "")}${endpoint}`;
  parsed.search = "";
  parsed.hash = "";
  return parsed.toString();
}

function tryJson(text) {
  try {
    return text ? JSON.parse(text) : {};
  } catch {
    return null;
  }
}

async function readResponse(response) {
  const text = await response.text();
  const parsed = tryJson(text);
  if (!response.ok) {
    throw new APIError(
      response.status,
      parsed?.error || `请求失败（HTTP ${response.status}）`,
      parsed?.code || "",
      parsed?.error ? "" : text.slice(0, 300),
    );
  }
  if (!parsed || typeof parsed !== "object") {
    throw new APIError(response.status, "服务端响应不是有效 JSON", "", text.slice(0, 300));
  }
  return parsed;
}

function networkError(error) {
  if (error instanceof APIError) return error;
  return new APIError(0, "无法连接服务器", "", error?.message || "");
}

async function requestPublic(baseUrl) {
  try {
    const response = await fetch(`${baseUrl}/api/sdk/version`, {
      headers: { Accept: "application/json" },
    });
    return await readResponse(response);
  } catch (error) {
    throw networkError(error);
  }
}

/** Query the public SDK metadata endpoint. No API key is required. */
export async function sdkVersion(baseUrl = "") {
  return requestPublic(resolveBaseUrl(baseUrl));
}

/** Append user join parameters to a ws:// or wss:// room URL. */
export function withQuery(rawUrl, params = {}) {
  let parsed;
  try {
    parsed = new URL(rawUrl);
  } catch {
    throw new Error("房间地址格式不正确");
  }
  if (!["ws:", "wss:"].includes(parsed.protocol)) {
    throw new Error("房间地址必须以 ws:// 或 wss:// 开头");
  }
  for (const [key, value] of Object.entries(params)) {
    parsed.searchParams.delete(key);
    for (const item of Array.isArray(value) ? value : [value]) {
      parsed.searchParams.append(key, String(item));
    }
  }
  return parsed.toString();
}

export class Client {
  constructor(apiKey = "", baseUrl = "") {
    this.apiKey = apiKey || process.env.XR_API_KEY || "";
    if (!this.apiKey) throw new Error("缺少 API Key：设置 XR_API_KEY 或传入 apiKey");
    this.baseUrl = resolveBaseUrl(baseUrl);
  }

  async sdkVersion() {
    return requestPublic(this.baseUrl);
  }

  async request(method, path, body) {
    const headers = {
      Accept: "application/json",
      Authorization: `Bearer ${this.apiKey}`,
    };
    const init = { method, headers };
    if (body !== undefined) {
      headers["Content-Type"] = "application/json";
      init.body = JSON.stringify(body);
    }
    let response;
    try {
      response = await fetch(this.baseUrl + path, init);
    } catch (error) {
      throw networkError(error);
    }
    return readResponse(response);
  }

  createRoom({ name = "", maxParticipants = 0 } = {}) {
    const body = {};
    if (name) body.name = name;
    if (maxParticipants) body.max_participants = maxParticipants;
    return this.request("POST", "/v1/rooms", body);
  }

  async listRooms() {
    const data = await this.request("GET", "/v1/rooms");
    return data.rooms || [];
  }

  async closeRoom(roomId) {
    try {
      return await this.request("DELETE", `/v1/rooms/${encodeURIComponent(roomId)}`);
    } catch (error) {
      if (error instanceof APIError && error.status === 404) return { ok: true };
      throw error;
    }
  }

  agentUrl() {
    return webSocketUrl(this.baseUrl, "/v1/agent");
  }

  createAgent(handler = {}) {
    return new Agent(this, handler);
  }
}

/**
 * Agent owns one auto-reconnecting control channel.
 *
 * Handler callbacks: authorize(request, agent), onHello(hello, agent),
 * onUserJoined/onUserLeft/onRoomClosed/onTrackPublished/onTrackUnpublished/
 * onSubscriptionsUpdated/onError(event, agent), and onDisconnect(error, retryInMs).
 * Event callbacks are serialized; authorize calls may run concurrently.
 */
export class Agent {
  #client;
  #handler;
  #closed = false;
  #running = false;
  #ws = null;
  #hello = null;
  #pending = new Map();
  #eventChain = Promise.resolve();

  constructor(client, handler = {}) {
    this.#client = client;
    this.#handler = handler || {};
  }

  get hello() {
    return this.#hello;
  }

  get connected() {
    return this.#ws?.readyState === WebSocket.OPEN;
  }

  async run() {
    if (this.#running) throw new Error("Agent 已在运行");
    this.#running = true;
    let backoff = RETRY_INITIAL_MS;
    try {
      while (!this.#closed) {
        let sawHello = false;
        try {
          sawHello = await this.#runOnce();
          if (this.#closed) return;
          if (sawHello) backoff = RETRY_INITIAL_MS;
          const error = new DisconnectedError();
          this.#notifyDisconnect(error, backoff);
          await sleep(backoff);
          if (!sawHello) backoff = Math.min(backoff * 2, RETRY_MAX_MS);
        } catch (error) {
          if (this.#closed) return;
          if (terminal(error)) {
            this.#notifyDisconnect(error, 0);
            throw error;
          }
          const sawHelloThisRun = Boolean(error?.sawHello);
          if (sawHelloThisRun) backoff = RETRY_INITIAL_MS;
          this.#notifyDisconnect(error, backoff);
          await sleep(backoff);
          if (!sawHelloThisRun) backoff = Math.min(backoff * 2, RETRY_MAX_MS);
        }
      }
    } finally {
      this.#running = false;
    }
  }

  close() {
    this.#closed = true;
    this.#rejectPending(new DisconnectedError("使用方通道已关闭"));
    const ws = this.#ws;
    if (ws) {
      try { ws.terminate(); } catch { /* already gone */ }
    }
  }

  subscribe(roomId, connId, { trackIds = [], all = false } = {}) {
    if (!roomId || !connId) {
      return Promise.reject(new Error("roomId 与 connId 不能为空"));
    }
    const ws = this.#ws;
    if (!ws || ws.readyState !== WebSocket.OPEN) {
      return Promise.reject(new DisconnectedError("使用方通道未连接"));
    }
    const requestId = `sub_${randomUUID()}`;
    return new Promise((resolve, reject) => {
      this.#pending.set(requestId, { resolve, reject });
      this.#send({
        type: "subscriptions",
        request_id: requestId,
        room_id: roomId,
        conn_id: connId,
        track_ids: Array.isArray(trackIds) ? trackIds : [],
        all: Boolean(all),
      }).catch((error) => {
        this.#pending.delete(requestId);
        reject(error);
      });
    });
  }

  async #runOnce() {
    const ws = new WebSocket(this.#client.agentUrl(), {
      headers: { Authorization: `Bearer ${this.#client.apiKey}` },
      handshakeTimeout: 10_000,
      maxPayload: 1 << 20,
      perMessageDeflate: false,
    });
    this.#ws = ws;

    await new Promise((resolve, reject) => {
      const cleanup = () => {
        ws.off("open", onOpen);
        ws.off("error", onError);
        ws.off("unexpected-response", onUnexpected);
      };
      const onOpen = () => {
        cleanup();
        resolve();
      };
      const onError = (error) => {
        cleanup();
        reject(networkError(error));
      };
      const onUnexpected = (_req, response) => {
        let text = "";
        response.setEncoding("utf8");
        response.on("data", (chunk) => {
          if (text.length < 1_000) text += chunk;
        });
        response.on("end", () => {
          cleanup();
          try { ws.terminate(); } catch { /* already gone */ }
          const parsed = tryJson(text);
          const status = response.statusCode || 0;
          reject(new APIError(
            status,
            parsed?.error || `连接使用方通道失败（HTTP ${status}）`,
            parsed?.code || "",
            "",
          ));
        });
      };
      ws.on("open", onOpen);
      ws.on("error", onError);
      ws.on("unexpected-response", onUnexpected);
    }).catch((error) => {
      if (this.#ws === ws) this.#ws = null;
      try { ws.terminate(); } catch { /* already gone */ }
      throw error;
    });

    if (this.#closed) {
      try { ws.terminate(); } catch { /* already gone */ }
      if (this.#ws === ws) this.#ws = null;
      return false;
    }

    let sawHello = false;
    try {
      await new Promise((resolve, reject) => {
        let settled = false;
        const cleanup = () => {
          ws.off("message", onMessage);
          ws.off("close", onClose);
          ws.off("error", onError);
        };
        const finish = (fn, value) => {
          if (settled) return;
          settled = true;
          cleanup();
          this.#ws = null;
          this.#rejectPending(new DisconnectedError());
          fn(value);
        };
        const onMessage = (data, isBinary) => {
          if (isBinary) return;
          let message;
          try {
            message = JSON.parse(data.toString());
          } catch {
            return;
          }
          this.#handleMessage(message, () => { sawHello = true; });
        };
        const onClose = (code) => {
          if (code === 4001) return finish(reject, new AgentReplacedError());
          if (code === 4003) return finish(reject, new KeyRevokedError());
          finish(resolve, sawHello);
        };
        const onError = (error) => finish(reject, networkError(error));
        ws.on("message", onMessage);
        ws.on("close", onClose);
        ws.on("error", onError);
      });
      return sawHello;
    } catch (error) {
      try { ws.terminate(); } catch { /* already gone */ }
      error.sawHello = sawHello;
      throw error;
    }
  }

  #handleMessage(message, onHello) {
    switch (message.type) {
      case "hello":
        this.#hello = message;
        onHello();
        this.#dispatch("onHello", message);
        break;
      case "auth_request":
        if (message.request_id) void this.#authorize(message);
        break;
      case "user_joined":
        this.#dispatch("onUserJoined", message);
        break;
      case "user_left":
        this.#dispatch("onUserLeft", message);
        break;
      case "room_closed":
        this.#dispatch("onRoomClosed", message);
        break;
      case "track_published":
        this.#dispatch("onTrackPublished", message);
        break;
      case "track_unpublished":
        this.#dispatch("onTrackUnpublished", message);
        break;
      case "subscriptions_updated": {
        const pending = this.#pending.get(message.request_id);
        if (pending) {
          this.#pending.delete(message.request_id);
          pending.resolve(message);
        }
        this.#dispatch("onSubscriptionsUpdated", message);
        break;
      }
      case "error": {
        const pending = message.request_id ? this.#pending.get(message.request_id) : null;
        if (pending) {
          this.#pending.delete(message.request_id);
          pending.reject(new Error(message.error || "控制通道返回错误"));
        }
        this.#dispatch("onError", message);
        break;
      }
      case "ping":
        this.#send({ type: "pong", time: new Date().toISOString() }).catch(() => {});
        break;
      default:
        break;
    }
  }

  async #authorize(request) {
    let decision = { allow: false, reason: "使用方未配置鉴权处理器" };
    if (typeof this.#handler.authorize === "function") {
      try {
        decision = await this.#handler.authorize(request, this);
      } catch {
        decision = { allow: false, reason: "鉴权处理器异常" };
      }
    }
    if (typeof decision === "boolean") decision = { allow: decision };
    const response = {
      type: "auth_response",
      request_id: request.request_id,
      allow: Boolean(decision?.allow),
    };
    if (decision?.subject) response.subject = String(decision.subject);
    if (decision?.reason) response.reason = String(decision.reason);
    await this.#send(response).catch(() => {});
  }

  #dispatch(name, event) {
    const callback = this.#handler?.[name];
    if (typeof callback !== "function") return;
    this.#eventChain = this.#eventChain
      .then(() => callback(event, this))
      .catch(() => {
        // A consumer callback must not break the control channel.
      });
  }

  #send(payload) {
    const ws = this.#ws;
    if (!ws || ws.readyState !== WebSocket.OPEN) {
      return Promise.reject(new DisconnectedError("使用方通道未连接"));
    }
    return new Promise((resolve, reject) => {
      ws.send(JSON.stringify(payload), (error) => {
        if (error) reject(error);
        else resolve();
      });
    });
  }

  #rejectPending(error) {
    for (const pending of this.#pending.values()) pending.reject(error);
    this.#pending.clear();
  }

  #notifyDisconnect(error, retryInMs) {
    if (typeof this.#handler?.onDisconnect !== "function") return;
    try {
      this.#handler.onDisconnect(error, retryInMs);
    } catch {
      // Ignore a failing disconnect callback.
    }
  }
}

function terminal(error) {
  return error instanceof AgentReplacedError
    || error instanceof KeyRevokedError
    || (error instanceof APIError && error.status === 401);
}

function sleep(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}
