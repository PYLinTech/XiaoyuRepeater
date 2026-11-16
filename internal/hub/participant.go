package hub

import (
	"net"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	participantQueueSize    = 256
	participantReadTimeout  = 75 * time.Second
	participantPingInterval = 25 * time.Second
	participantWriteTimeout = 10 * time.Second
	participantCloseTimeout = 3 * time.Second
)

type Decision struct {
	Subject string
}

type Participant struct {
	ID         string
	Subject    string
	RemoteAddr string
	UserAgent  string
	JoinedAt   time.Time

	hub  *Hub
	room *Room
	ws   *websocket.Conn

	out        chan outgoing
	done       chan struct{}
	closeOnce  sync.Once
	finishOnce sync.Once
	reasonMu   sync.Mutex
	reason     string
}

type outgoing struct {
	messageType int
	data        []byte
}

func newParticipant(h *Hub, room *Room, ws *websocket.Conn, decision Decision, remoteAddr, userAgent string) *Participant {
	return &Participant{
		ID:         newID("c_", 6),
		Subject:    decision.Subject,
		RemoteAddr: remoteAddr,
		UserAgent:  userAgent,
		JoinedAt:   time.Now().UTC(),
		hub:        h,
		room:       room,
		ws:         ws,
		out:        make(chan outgoing, participantQueueSize),
		done:       make(chan struct{}),
	}
}

func (p *Participant) run() {
	go p.writeLoop()
	p.readLoop()
}

func (p *Participant) readLoop() {
	p.ws.SetReadLimit(p.hub.cfg.MaxFrameBytes)
	_ = p.ws.SetReadDeadline(time.Now().Add(participantReadTimeout))
	p.ws.SetPongHandler(func(string) error {
		return p.ws.SetReadDeadline(time.Now().Add(participantReadTimeout))
	})

	for {
		messageType, data, err := p.ws.ReadMessage()
		if err != nil {
			p.finish(readErrorReason(err))
			return
		}
		if messageType != websocket.TextMessage && messageType != websocket.BinaryMessage {
			continue
		}
		_ = p.ws.SetReadDeadline(time.Now().Add(participantReadTimeout))
		p.hub.relay(p, messageType, data)
	}
}

func (p *Participant) writeLoop() {
	ticker := time.NewTicker(participantPingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-p.done:
			return
		case msg := <-p.out:
			_ = p.ws.SetWriteDeadline(time.Now().Add(participantWriteTimeout))
			if err := p.ws.WriteMessage(msg.messageType, msg.data); err != nil {
				p.closeWith(websocket.CloseGoingAway, "write failed")
				return
			}
		case <-ticker.C:
			if err := p.ws.WriteControl(websocket.PingMessage, nil, time.Now().Add(participantWriteTimeout)); err != nil {
				p.closeWith(websocket.CloseGoingAway, "ping failed")
				return
			}
		}
	}
}

func (p *Participant) trySend(messageType int, data []byte) bool {
	select {
	case <-p.done:
		return false
	default:
	}
	select {
	case p.out <- outgoing{messageType: messageType, data: data}:
		return true
	case <-p.done:
		return false
	default:
		return false
	}
}

func (p *Participant) closeWith(code int, reason string) {
	p.closeOnce.Do(func() {
		close(p.done)
		_ = p.ws.WriteControl(
			websocket.CloseMessage,
			websocket.FormatCloseMessage(code, reason),
			time.Now().Add(participantCloseTimeout),
		)
		_ = p.ws.Close()
	})
}

func (p *Participant) finish(reason string) {
	p.finishOnce.Do(func() {
		p.reasonMu.Lock()
		p.reason = reason
		p.reasonMu.Unlock()
		p.closeWith(websocket.CloseNormalClosure, "closed")
		p.hub.removeParticipant(p, reason)
	})
}

func (p *Participant) finishReason() string {
	p.reasonMu.Lock()
	defer p.reasonMu.Unlock()
	return p.reason
}

func readErrorReason(err error) string {
	if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
		return "client_closed"
	}
	var netErr net.Error
	if ok := asNetError(err, &netErr); ok && netErr.Timeout() {
		return "timeout"
	}
	return "disconnected"
}

func asNetError(err error, target *net.Error) bool {
	for err != nil {
		if ne, ok := err.(net.Error); ok {
			*target = ne
			return true
		}
		type unwrapper interface{ Unwrap() error }
		u, ok := err.(unwrapper)
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
