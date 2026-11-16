package hub

import (
	"encoding/json"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"xiaoyurepeater/internal/protocol"
)

const (
	agentQueueSize    = 128
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
		case protocol.TypePing:
			a.sendJSON(protocol.Pong{Type: protocol.TypePong, Time: time.Now().UTC()})
		case protocol.TypePong:
		default:
			a.hub.log.Debug("忽略未知使用方消息", "type", envelope.Type, "agent_id", a.id)
		}
	}
}

func (a *Agent) deliverAuth(resp protocol.AuthResponse) {
	if resp.RequestID == "" {
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
