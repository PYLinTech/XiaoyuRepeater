package hub

import (
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"xiaoyurepeater/internal/protocol"
)

const (
	// 订阅确认与房间事件可能在同一轮恢复中集中到达，队列过小会把正常突发
	// 误判为慢使用方并断开鉴权通道。
	agentQueueSize    = 1024
	agentPendingLimit = 256
	agentReadLimit    = 256 << 10
	agentReadTimeout  = 75 * time.Second
	agentPingInterval = 20 * time.Second
	agentWriteTimeout = 10 * time.Second
	agentCloseTimeout = 3 * time.Second
)

// AuthOutcome is passed from the agent reader to the waiting join request.
type AuthOutcome struct {
	Decision Decision
	Err      *Error
}

type Agent struct {
	hub        *Hub
	id         string
	keyID      string
	keyName    string
	remoteAddr string
	connected  time.Time
	ws         *websocket.Conn
	out        chan []byte
	subs       chan protocol.SubscriptionsCommand
	done       chan struct{}
	closeOnce  sync.Once

	pendingMu sync.Mutex
	pending   map[string]chan AuthOutcome
}

func newAgent(h *Hub, ws *websocket.Conn, keyID, keyName, remoteAddr string) *Agent {
	return &Agent{
		hub:        h,
		id:         newID("ag_", 8),
		keyID:      keyID,
		keyName:    keyName,
		remoteAddr: remoteAddr,
		connected:  time.Now().UTC(),
		ws:         ws,
		out:        make(chan []byte, agentQueueSize),
		subs:       make(chan protocol.SubscriptionsCommand, h.cfg.AgentSubscriptionQueue),
		done:       make(chan struct{}),
		pending:    make(map[string]chan AuthOutcome),
	}
}

func (a *Agent) sendJSON(v any) bool {
	raw, err := json.Marshal(v)
	if err != nil {
		a.hub.log.Error("序列化使用方消息失败", "error", err, "agent_id", a.id)
		return false
	}
	select {
	case <-a.done:
		return false
	default:
	}
	select {
	case a.out <- raw:
		return true
	case <-a.done:
		return false
	default:
		a.hub.log.Warn("使用方发送队列已满，断开连接", "agent_id", a.id, "key_id", a.keyID)
		a.closeWith(websocket.ClosePolicyViolation, "outbound queue overflow")
		return false
	}
}

func (a *Agent) writeLoop() {
	ticker := time.NewTicker(agentPingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-a.done:
			return
		case raw := <-a.out:
			_ = a.ws.SetWriteDeadline(time.Now().Add(agentWriteTimeout))
			if err := a.ws.WriteMessage(websocket.TextMessage, raw); err != nil {
				a.closeWith(websocket.CloseGoingAway, "write failed")
				return
			}
		case <-ticker.C:
			if err := a.ws.WriteControl(websocket.PingMessage, nil, time.Now().Add(agentWriteTimeout)); err != nil {
				a.closeWith(websocket.CloseGoingAway, "ping failed")
				return
			}
		}
	}
}

func (a *Agent) readLoop() {
	a.ws.SetReadLimit(agentReadLimit)
	_ = a.ws.SetReadDeadline(time.Now().Add(agentReadTimeout))
	a.ws.SetPongHandler(func(string) error {
		return a.ws.SetReadDeadline(time.Now().Add(agentReadTimeout))
	})

	for {
		msgType, raw, err := a.ws.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err,
				websocket.CloseNormalClosure,
				websocket.CloseGoingAway,
				websocket.CloseNoStatusReceived,
				websocket.ClosePolicyViolation,
				websocket.CloseServiceRestart,
			) {
				a.hub.log.Warn("使用方连接读取失败", "error", err, "agent_id", a.id, "key_id", a.keyID)
			}
			return
		}
		if msgType != websocket.TextMessage {
			a.hub.log.Warn("忽略使用方非文本消息", "agent_id", a.id)
			continue
		}
		_ = a.ws.SetReadDeadline(time.Now().Add(agentReadTimeout))
		var envelope struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(raw, &envelope); err != nil {
			a.hub.log.Warn("使用方消息不是有效 JSON", "error", err, "agent_id", a.id)
			continue
		}
		switch envelope.Type {
		case protocol.TypeAuthResponse:
			var resp protocol.AuthResponse
			if err := json.Unmarshal(raw, &resp); err != nil {
				a.hub.log.Warn("解析鉴权响应失败", "error", err, "agent_id", a.id)
				continue
			}
			a.deliverAuth(resp)
		case protocol.TypeSubscriptions:
			var cmd protocol.SubscriptionsCommand
			if err := json.Unmarshal(raw, &cmd); err != nil {
				a.hub.log.Warn("解析订阅命令失败", "error", err, "agent_id", a.id)
				continue
			}
			a.enqueueSubscriptions(cmd)
		case protocol.TypeDisconnect:
			var cmd protocol.DisconnectCommand
			if err := json.Unmarshal(raw, &cmd); err != nil {
				a.hub.log.Warn("解析断开连接命令失败", "error", err, "agent_id", a.id)
				continue
			}
			go a.handleDisconnect(cmd)
		case protocol.TypePing:
			a.sendJSON(protocol.Pong{Type: protocol.TypePong, Time: time.Now().UTC()})
		case protocol.TypePong:
		default:
			a.hub.log.Debug("忽略未知使用方消息", "type", envelope.Type, "agent_id", a.id)
		}
	}
}

// subscriptionLoop keeps peer-actor waits off the reader goroutine so auth,
// pong and ping handling stay responsive during subscription bursts.
func (a *Agent) subscriptionLoop() {
	for !a.isClosed() {
		select {
		case <-a.done:
			return
		case cmd := <-a.subs:
			a.handleSubscriptions(cmd)
		}
	}
}

func (a *Agent) enqueueSubscriptions(cmd protocol.SubscriptionsCommand) {
	if a.isClosed() {
		return
	}
	select {
	case a.subs <- cmd:
	case <-a.done:
	default:
		a.sendCommandError(cmd, "订阅命令过多，请稍后重试")
	}
}

func (a *Agent) handleSubscriptions(cmd protocol.SubscriptionsCommand) {
	if cmd.RequestID == "" {
		a.sendCommandError(cmd, "request_id 不能为空")
		return
	}
	if cmd.RoomID == "" || cmd.ConnID == "" {
		a.sendCommandError(cmd, "room_id 和 conn_id 不能为空")
		return
	}

	a.hub.mu.RLock()
	room := a.hub.rooms[cmd.RoomID]
	owned := room != nil && room.KeyID == a.keyID && a.hub.agents[a.keyID] == a
	a.hub.mu.RUnlock()
	if !owned {
		a.sendCommandError(cmd, "房间不存在或不属于当前密钥")
		return
	}
	if room.isClosed() {
		a.sendCommandError(cmd, "房间已关闭")
		return
	}
	peer := room.peerByID(cmd.ConnID)
	if peer == nil || peer.isClosed() {
		a.sendCommandError(cmd, "WebRTC 参与者不存在或已离开")
		return
	}

	result, ok := peer.applySubscriptions(cmd.All, cmd.TrackIDs, cmd.RequestID, cmd.Revision)
	if !ok {
		a.sendCommandError(cmd, "WebRTC 参与者已断开或订阅应用超时")
		return
	}
	room.touch()
	a.sendJSON(protocol.SubscriptionsUpdate{
		Type:      protocol.TypeSubscriptionsUpdated,
		RequestID: cmd.RequestID,
		RoomID:    cmd.RoomID,
		ConnID:    cmd.ConnID,
		TrackIDs:  result.normalized,
		All:       result.all,
		Revision:  result.revision,
		Applied:   result.applied,
		Pending:   result.pending,
		Duplicate: result.duplicate,
		Stale:     result.stale,
	})
	if result.duplicate {
		a.hub.log.Debug("忽略重复的订阅命令",
			"room_id", cmd.RoomID, "peer_id", cmd.ConnID, "request_id", cmd.RequestID)
	}
}

func (a *Agent) sendCommandError(cmd protocol.SubscriptionsCommand, message string) {
	a.sendJSON(protocol.AgentError{
		Type:      protocol.TypeError,
		RequestID: cmd.RequestID,
		RoomID:    cmd.RoomID,
		ConnID:    cmd.ConnID,
		Error:     message,
	})
}

func (a *Agent) handleDisconnect(cmd protocol.DisconnectCommand) {
	if cmd.RequestID == "" {
		a.sendJSON(protocol.AgentError{
			Type:   protocol.TypeError,
			RoomID: cmd.RoomID,
			ConnID: cmd.ConnID,
			Error:  "request_id 不能为空",
		})
		return
	}
	if cmd.RoomID == "" || cmd.ConnID == "" {
		a.sendJSON(protocol.AgentError{
			Type:      protocol.TypeError,
			RequestID: cmd.RequestID,
			RoomID:    cmd.RoomID,
			ConnID:    cmd.ConnID,
			Error:     "room_id 和 conn_id 不能为空",
		})
		return
	}

	a.hub.mu.RLock()
	room := a.hub.rooms[cmd.RoomID]
	owned := room != nil && room.KeyID == a.keyID && a.hub.agents[a.keyID] == a
	a.hub.mu.RUnlock()
	if !owned {
		a.sendJSON(protocol.AgentError{
			Type:      protocol.TypeError,
			RequestID: cmd.RequestID,
			RoomID:    cmd.RoomID,
			ConnID:    cmd.ConnID,
			Error:     "房间不存在或不属于当前密钥",
		})
		return
	}
	if room.isClosed() {
		a.sendJSON(protocol.AgentError{
			Type:      protocol.TypeError,
			RequestID: cmd.RequestID,
			RoomID:    cmd.RoomID,
			ConnID:    cmd.ConnID,
			Error:     "房间已关闭",
		})
		return
	}

	code := cmd.Code
	if code != websocket.CloseNormalClosure && (code < 4000 || code > 4999) {
		code = 4001
	}
	reason := cleanString(strings.TrimSpace(cmd.Reason), 120, "disconnected by consumer")

	closed := false
	if peer := room.peerByID(cmd.ConnID); peer != nil && !peer.isClosed() {
		peer.closeWith(code, reason)
		peer.finish("consumer_disconnected")
		closed = true
	} else if part := room.participantByID(cmd.ConnID); part != nil {
		part.closeWith(code, reason)
		part.finish("consumer_disconnected")
		closed = true
	}

	a.sendJSON(protocol.DisconnectedResult{
		Type:      protocol.TypeDisconnected,
		RequestID: cmd.RequestID,
		RoomID:    cmd.RoomID,
		ConnID:    cmd.ConnID,
		Closed:    closed,
	})
}

func (a *Agent) deliverAuth(resp protocol.AuthResponse) {
	if resp.RequestID == "" {
		return
	}
	a.hub.mu.RLock()
	current := a.hub.agents[a.keyID] == a
	a.hub.mu.RUnlock()
	if !current {
		return
	}
	a.pendingMu.Lock()
	ch, ok := a.pending[resp.RequestID]
	if ok {
		delete(a.pending, resp.RequestID)
	}
	a.pendingMu.Unlock()
	if !ok {
		return
	}
	allow := resp.Allow != nil && *resp.Allow
	outcome := AuthOutcome{}
	if !allow {
		outcome.Err = denied(resp.Reason)
	} else {
		outcome.Decision = Decision{
			Subject: cleanString(resp.Subject, 128, "用户"),
		}
	}
	select {
	case ch <- outcome:
	default:
	}
}

func (a *Agent) addPending(id string) (chan AuthOutcome, bool) {
	a.pendingMu.Lock()
	defer a.pendingMu.Unlock()
	if a.isClosed() {
		return nil, false
	}
	if len(a.pending) >= agentPendingLimit {
		return nil, false
	}
	ch := make(chan AuthOutcome, 1)
	a.pending[id] = ch
	return ch, true
}

func (a *Agent) removePending(id string, ch chan AuthOutcome) {
	a.pendingMu.Lock()
	if cur, ok := a.pending[id]; ok && cur == ch {
		delete(a.pending, id)
	}
	a.pendingMu.Unlock()
}

func (a *Agent) failPending(err *Error) {
	a.pendingMu.Lock()
	pending := a.pending
	a.pending = make(map[string]chan AuthOutcome)
	a.pendingMu.Unlock()
	for _, ch := range pending {
		select {
		case ch <- AuthOutcome{Err: err}:
		default:
		}
	}
}

func (a *Agent) isClosed() bool {
	select {
	case <-a.done:
		return true
	default:
		return false
	}
}

func (a *Agent) closeWith(code int, reason string) {
	a.closeOnce.Do(func() {
		close(a.done)
		_ = a.ws.WriteControl(
			websocket.CloseMessage,
			websocket.FormatCloseMessage(code, reason),
			time.Now().Add(agentCloseTimeout),
		)
		_ = a.ws.Close()
	})
}

func cleanString(v string, maxRunes int, fallback string) string {
	runes := []rune(v)
	if len(runes) == 0 {
		return fallback
	}
	if len(runes) > maxRunes {
		runes = runes[:maxRunes]
	}
	return string(runes)
}
