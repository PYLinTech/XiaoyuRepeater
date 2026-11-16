package xiaoyurepeater

import (
	"context"
	crand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

var (
	// ErrNotConnected means the agent channel is not currently connected.
	ErrNotConnected = errors.New("使用方通道未连接")
	// ErrDisconnected means the channel closed while a command was waiting.
	ErrDisconnected = errors.New("使用方通道已断开")
	// ErrAgentReplaced means another connection took over this API key.
	ErrAgentReplaced = errors.New("使用方通道已被新连接替换")
	// ErrKeyRevoked means the API key was deleted.
	ErrKeyRevoked = errors.New("API 密钥已被撤销")
	// ErrWebRTCUnavailable means the room was created without WebRTC.
	ErrWebRTCUnavailable = errors.New("WebRTC 数据面未开启")
)

const (
	agentReadLimit    = 1 << 20
	agentReadTimeout  = 90 * time.Second
	agentWriteTimeout = 10 * time.Second
	agentEventQueue   = 128
	agentRetryInitial = time.Second
	agentRetryMaximum = 30 * time.Second
)

// AgentHandler receives agent-channel callbacks. All callbacks are optional.
//
// OnHello and the event callbacks are invoked in order by one dispatcher
// goroutine, while Authorize calls run concurrently so a slow authorization
// does not stall the channel. Callbacks should return promptly. The Agent
// pointer may be used for SetSubscriptions from any callback.
type AgentHandler struct {
	OnHello                func(ctx context.Context, agent *Agent, hello Hello)
	Authorize              func(ctx context.Context, agent *Agent, request AuthRequest) AuthDecision
	OnUserJoined           func(ctx context.Context, agent *Agent, event UserJoined)
	OnUserLeft             func(ctx context.Context, agent *Agent, event UserLeft)
	OnRoomClosed           func(ctx context.Context, agent *Agent, event RoomClosed)
	OnTrackPublished       func(ctx context.Context, agent *Agent, event TrackPublished)
	OnTrackUnpublished     func(ctx context.Context, agent *Agent, event TrackUnpublished)
	OnSubscriptionsUpdated func(ctx context.Context, agent *Agent, event SubscriptionsUpdate)
	OnError                func(ctx context.Context, agent *Agent, event AgentError)
	// OnDisconnect runs after a channel loss. retryIn is the delay before the
	// next connection attempt, or 0 when the SDK is stopping terminally.
	OnDisconnect func(err error, retryIn time.Duration)
}

type agentConn struct {
	ws        *websocket.Conn
	writeMu   sync.Mutex
	events    chan func(context.Context)
	done      chan struct{}
	closeOnce sync.Once
}

func (c *agentConn) isClosed() bool {
	select {
	case <-c.done:
		return true
	default:
		return false
	}
}

func (c *agentConn) close() {
	c.closeOnce.Do(func() {
		close(c.done)
		_ = c.ws.Close()
	})
}

func (c *agentConn) writeJSON(value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.isClosed() {
		return ErrDisconnected
	}
	_ = c.ws.SetWriteDeadline(time.Now().Add(agentWriteTimeout))
	return c.ws.WriteMessage(websocket.TextMessage, raw)
}

type commandResult struct {
	update SubscriptionsUpdate
	err    error
}

// Agent owns one auto-reconnecting agent channel for a Client.
type Agent struct {
	client  *Client
	handler AgentHandler

	mu       sync.Mutex
	conn     *agentConn
	hello    Hello
	hasHello bool
	pending  map[string]chan commandResult
	cancel   context.CancelFunc
	running  bool
}

// NewAgent creates an agent. Call Run to connect and keep it alive.
func (c *Client) NewAgent(handler AgentHandler) *Agent {
	return &Agent{
		client:  c,
		handler: handler,
		pending: make(map[string]chan commandResult),
	}
}

// RunAgent is a convenience for NewAgent(handler).Run(ctx).
func (c *Client) RunAgent(ctx context.Context, handler AgentHandler) error {
	return c.NewAgent(handler).Run(ctx)
}

// Run connects and blocks until ctx is canceled or a terminal error occurs.
// It reconnects automatically after ordinary disconnects and calls OnHello
// again with a fresh snapshot after every reconnect.
func (a *Agent) Run(ctx context.Context) error {
	runCtx, cancel := context.WithCancel(ctx)
	a.mu.Lock()
	if a.running {
		a.mu.Unlock()
		cancel()
		return errors.New("Agent 已在运行")
	}
	a.running = true
	a.cancel = cancel
	a.mu.Unlock()

	defer func() {
		cancel()
		a.mu.Lock()
		a.running = false
		a.cancel = nil
		a.mu.Unlock()
	}()

	backoff := agentRetryInitial
	for {
		if runCtx.Err() != nil {
			return nil
		}
		sawHello, err := a.runOnce(runCtx)
		if runCtx.Err() != nil {
			return nil
		}
		if err == nil {
			err = ErrDisconnected
		}
		if terminalAgentError(err) {
			a.notifyDisconnect(err, 0)
			return err
		}
		if sawHello {
			backoff = agentRetryInitial
		}
		retryIn := backoff
		a.notifyDisconnect(err, retryIn)
		timer := time.NewTimer(retryIn)
		select {
		case <-runCtx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
		if !sawHello {
			backoff *= 2
			if backoff > agentRetryMaximum {
				backoff = agentRetryMaximum
			}
		}
	}
}

// Close stops a running agent. Run then returns nil.
func (a *Agent) Close() error {
	a.mu.Lock()
	cancel := a.cancel
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return nil
}

// Connected reports whether an agent WebSocket is currently open.
func (a *Agent) Connected() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.conn != nil && !a.conn.isClosed()
}

// Hello returns the most recent hello snapshot.
func (a *Agent) Hello() (Hello, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.hello, a.hasHello
}

// SetSubscriptions replaces the complete subscription state of one WebRTC
// participant and waits for the relay's confirmation.
func (a *Agent) SetSubscriptions(ctx context.Context, roomID, connID string, trackIDs []string, all bool) (SubscriptionsUpdate, error) {
	if roomID == "" || connID == "" {
		return SubscriptionsUpdate{}, errors.New("roomID 与 connID 不能为空")
	}
	requestID := newSubscriptionRequestID()
	result := make(chan commandResult, 1)

	a.mu.Lock()
	ac := a.conn
	if ac == nil || ac.isClosed() {
		a.mu.Unlock()
		return SubscriptionsUpdate{}, ErrNotConnected
	}
	a.pending[requestID] = result
	a.mu.Unlock()

	if trackIDs == nil {
		trackIDs = []string{}
	}
	command := SubscriptionsCommand{
		Type:      "subscriptions",
		RequestID: requestID,
		RoomID:    roomID,
		ConnID:    connID,
		TrackIDs:  trackIDs,
		All:       all,
	}
	if err := ac.writeJSON(command); err != nil {
		a.removePending(requestID)
		return SubscriptionsUpdate{}, err
	}

	select {
	case outcome := <-result:
		if outcome.err != nil {
			return SubscriptionsUpdate{}, outcome.err
		}
		return outcome.update, nil
	case <-ctx.Done():
		a.removePending(requestID)
		return SubscriptionsUpdate{}, ctx.Err()
	case <-ac.done:
		a.removePending(requestID)
		return SubscriptionsUpdate{}, ErrDisconnected
	}
}

func (a *Agent) runOnce(ctx context.Context) (bool, error) {
	endpoint, err := a.client.AgentURL()
	if err != nil {
		return false, err
	}
	dialer := websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
		ReadBufferSize:   4096,
		WriteBufferSize:  4096,
	}
	header := http.Header{}
	header.Set("Authorization", "Bearer "+a.client.APIKey)
	ws, resp, err := dialer.DialContext(ctx, endpoint, header)
	if err != nil {
		if resp != nil {
			raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
			_ = resp.Body.Close()
			return false, apiErrorFromBody(resp.StatusCode, raw)
		}
		return false, fmt.Errorf("连接使用方通道失败: %w", err)
	}

	ac := &agentConn{
		ws:     ws,
		events: make(chan func(context.Context), agentEventQueue),
		done:   make(chan struct{}),
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	a.mu.Lock()
	a.conn = ac
	a.mu.Unlock()
	defer func() {
		ac.close()
		a.mu.Lock()
		if a.conn == ac {
			a.conn = nil
		}
		a.mu.Unlock()
		a.failPending(ErrDisconnected)
	}()

	go a.runEvents(runCtx, ac)
	go func() {
		select {
		case <-runCtx.Done():
			ac.close()
		case <-ac.done:
		}
	}()

	ws.SetReadLimit(agentReadLimit)
	_ = ws.SetReadDeadline(time.Now().Add(agentReadTimeout))
	ws.SetPingHandler(func(data string) error {
		_ = ws.SetReadDeadline(time.Now().Add(agentReadTimeout))
		return ws.WriteControl(websocket.PongMessage, []byte(data), time.Now().Add(agentWriteTimeout))
	})

	sawHello := false
	for {
		messageType, data, readErr := ws.ReadMessage()
		if readErr != nil {
			return sawHello, classifyAgentClose(readErr)
		}
		if messageType != websocket.TextMessage {
			continue
		}
		var envelope struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(data, &envelope) != nil {
			continue
		}
		switch envelope.Type {
		case "hello":
			var hello Hello
			if json.Unmarshal(data, &hello) != nil {
				continue
			}
			sawHello = true
			a.mu.Lock()
			a.hello = hello
			a.hasHello = true
			a.mu.Unlock()
			current := hello
			a.dispatch(runCtx, ac, func(ctx context.Context) {
				if a.handler.OnHello != nil {
					a.handler.OnHello(ctx, a, current)
				}
			})
		case "auth_request":
			var request AuthRequest
			if json.Unmarshal(data, &request) != nil || request.RequestID == "" {
				continue
			}
			go a.authorize(runCtx, ac, request)
		case "user_joined":
			var event UserJoined
			if json.Unmarshal(data, &event) == nil {
				a.dispatch(runCtx, ac, func(ctx context.Context) {
					if a.handler.OnUserJoined != nil {
						a.handler.OnUserJoined(ctx, a, event)
					}
				})
			}
		case "user_left":
			var event UserLeft
			if json.Unmarshal(data, &event) == nil {
				a.dispatch(runCtx, ac, func(ctx context.Context) {
					if a.handler.OnUserLeft != nil {
						a.handler.OnUserLeft(ctx, a, event)
					}
				})
			}
		case "room_closed":
			var event RoomClosed
			if json.Unmarshal(data, &event) == nil {
				a.dispatch(runCtx, ac, func(ctx context.Context) {
					if a.handler.OnRoomClosed != nil {
						a.handler.OnRoomClosed(ctx, a, event)
					}
				})
			}
		case "track_published":
			var event TrackPublished
			if json.Unmarshal(data, &event) == nil {
				a.dispatch(runCtx, ac, func(ctx context.Context) {
					if a.handler.OnTrackPublished != nil {
						a.handler.OnTrackPublished(ctx, a, event)
					}
				})
			}
		case "track_unpublished":
			var event TrackUnpublished
			if json.Unmarshal(data, &event) == nil {
				a.dispatch(runCtx, ac, func(ctx context.Context) {
					if a.handler.OnTrackUnpublished != nil {
						a.handler.OnTrackUnpublished(ctx, a, event)
					}
				})
			}
		case "subscriptions_updated":
			var event SubscriptionsUpdate
			if json.Unmarshal(data, &event) == nil {
				a.resolveSubscription(event)
				a.dispatch(runCtx, ac, func(ctx context.Context) {
					if a.handler.OnSubscriptionsUpdated != nil {
						a.handler.OnSubscriptionsUpdated(ctx, a, event)
					}
				})
			}
		case "error":
			var event AgentError
			if json.Unmarshal(data, &event) == nil {
				a.rejectSubscription(event)
				a.dispatch(runCtx, ac, func(ctx context.Context) {
					if a.handler.OnError != nil {
						a.handler.OnError(ctx, a, event)
					}
				})
			}
		case "ping":
			_ = ac.writeJSON(map[string]any{
				"type": "pong",
				"time": time.Now().UTC(),
			})
		case "pong":
		}
	}
}

func (a *Agent) authorize(ctx context.Context, ac *agentConn, request AuthRequest) {
	decision := Deny("使用方未配置鉴权处理器")
	if a.handler.Authorize != nil {
		timeout := a.authTimeout()
		handlerCtx, cancel := context.WithTimeout(ctx, timeout)
		func() {
			defer func() {
				if recover() != nil {
					decision = Deny("鉴权处理器异常")
				}
			}()
			decision = a.handler.Authorize(handlerCtx, a, request)
		}()
		cancel()
	}
	if ctx.Err() != nil || ac.isClosed() {
		return
	}
	allow := decision.Allow
	_ = ac.writeJSON(AuthResponse{
		Type:      "auth_response",
		RequestID: request.RequestID,
		Allow:     &allow,
		Subject:   decision.Subject,
		Reason:    decision.Reason,
	})
}

func (a *Agent) authTimeout() time.Duration {
	a.mu.Lock()
	millis := a.hello.Limits.AuthTimeoutMillis
	a.mu.Unlock()
	if millis <= 0 {
		return 10 * time.Second
	}
	return time.Duration(millis) * time.Millisecond
}

func (a *Agent) dispatch(ctx context.Context, ac *agentConn, fn func(context.Context)) {
	if fn == nil {
		return
	}
	select {
	case ac.events <- fn:
	case <-ctx.Done():
	case <-ac.done:
	}
}

func (a *Agent) runEvents(ctx context.Context, ac *agentConn) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-ac.done:
			return
		case fn := <-ac.events:
			func() {
				defer func() { _ = recover() }()
				fn(ctx)
			}()
		}
	}
}

func (a *Agent) resolveSubscription(event SubscriptionsUpdate) {
	a.mu.Lock()
	result := a.pending[event.RequestID]
	if result != nil {
		delete(a.pending, event.RequestID)
	}
	a.mu.Unlock()
	if result != nil {
		select {
		case result <- commandResult{update: event}:
		default:
		}
	}
}

func (a *Agent) rejectSubscription(event AgentError) {
	if event.RequestID == "" {
		return
	}
	a.mu.Lock()
	result := a.pending[event.RequestID]
	if result != nil {
		delete(a.pending, event.RequestID)
	}
	a.mu.Unlock()
	if result != nil {
		select {
		case result <- commandResult{err: errors.New(event.Error)}:
		default:
		}
	}
}

func (a *Agent) removePending(requestID string) {
	a.mu.Lock()
	delete(a.pending, requestID)
	a.mu.Unlock()
}

func (a *Agent) failPending(err error) {
	a.mu.Lock()
	pending := a.pending
	a.pending = make(map[string]chan commandResult)
	a.mu.Unlock()
	for _, result := range pending {
		select {
		case result <- commandResult{err: err}:
		default:
		}
	}
}

func (a *Agent) notifyDisconnect(err error, retryIn time.Duration) {
	if a.handler.OnDisconnect == nil {
		return
	}
	func() {
		defer func() { _ = recover() }()
		a.handler.OnDisconnect(err, retryIn)
	}()
}

func terminalAgentError(err error) bool {
	if errors.Is(err, ErrAgentReplaced) || errors.Is(err, ErrKeyRevoked) {
		return true
	}
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.Status == http.StatusUnauthorized
}

func classifyAgentClose(err error) error {
	if websocket.IsCloseError(err, 4001) {
		return ErrAgentReplaced
	}
	if websocket.IsCloseError(err, 4003) {
		return ErrKeyRevoked
	}
	return fmt.Errorf("读取使用方通道失败: %w", err)
}

func newSubscriptionRequestID() string {
	var random [8]byte
	if _, err := crand.Read(random[:]); err == nil {
		return "sub_" + hex.EncodeToString(random[:])
	}
	return fmt.Sprintf("sub_%d", time.Now().UnixNano())
}
