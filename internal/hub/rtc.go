package hub

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"

	"xiaoyurepeater/internal/protocol"
)

const (
	peerReadLimit    = 512 << 10
	peerReadTimeout  = 75 * time.Second
	peerPingInterval = 25 * time.Second
	peerWriteTimeout = 10 * time.Second
	peerCloseTimeout = 3 * time.Second

	peerOfferTimeout    = 15 * time.Second
	peerTickInterval    = 200 * time.Millisecond
	peerCommandQueue    = 256
	peerCommandWait     = 5 * time.Second
	peerDesiredPruneGap = 5 * time.Second

	keyframeGap      = time.Second
	keyframeBackoff  = 500 * time.Millisecond
	keyframeMaxDelay = 10 * time.Second

	iceDisconnectGrace = 5 * time.Second
	iceRestartBase     = 2 * time.Second
	iceRestartMax      = 30 * time.Second
	iceFailureTTL      = 2 * time.Minute

	offerRetryBase = time.Second
	offerRetryMax  = 15 * time.Second

	downlinkRetryDelay = 5 * time.Second

	keyframeInterval = 500 * time.Millisecond
	peerQueueSize    = 128
)

var errPeerClosed = errors.New("WebRTC 参与者已关闭")

type peerCommandKind int

const (
	cmdOffer peerCommandKind = iota
	cmdAnswer
	cmdICE
	cmdPublish
	cmdTrackAdded
	cmdTrackRemoved
	cmdTrackReleased
	cmdDownlinkFailed
	cmdReconcile
	cmdSubscriptions
	cmdRequestKeyframe
	cmdConnectionState
)

type peerCommand struct {
	kind      peerCommandKind
	sdp       string
	candidate *webrtc.ICECandidateInit
	track     *publishedTrack
	remote    *webrtc.TrackRemote
	receiver  *webrtc.RTPReceiver
	key       string
	ids       []string
	all       bool
	requestID string
	revision  int64
	state     webrtc.PeerConnectionState
	reply     chan error
	replySub  chan subscriptionApply
}

type subscriptionApply struct {
	normalized []string
	applied    []string
	pending    []string
	all        bool
	revision   int64
	duplicate  bool
	stale      bool
	ok         bool
}

// RTCPeer is a room member whose media is carried by a Pion PeerConnection.
// The signaling WebSocket only carries SDP/ICE JSON; RTP bypasses it.
//
// All PeerConnection mutations and all subscription state are owned by one
// actor goroutine (actorLoop). Other goroutines only enqueue commands or read
// atomic gauges, so no Pion call ever runs under a shared lock.
type RTCPeer struct {
	ID         string
	Subject    string
	RemoteAddr string
	UserAgent  string
	JoinedAt   time.Time

	hub  *Hub
	room *Room

	ws *websocket.Conn
	pc *webrtc.PeerConnection

	out        chan []byte
	cmds       chan peerCommand
	done       chan struct{}
	closeOnce  sync.Once
	finishOnce sync.Once
	reasonMu   sync.Mutex
	reason     string
	closed     atomic.Bool

	downlinks atomic.Int32
	published atomic.Int32
	resync    atomic.Bool
	snap      atomic.Pointer[protocol.SubscriptionState]

	state peerState
}

// peerState is owned exclusively by actorLoop.
type peerState struct {
	remoteSet  bool
	pendingICE []webrtc.ICECandidateInit

	initialSynced bool
	all           bool
	ids           []string
	wanted        map[string]struct{}
	desiredAt     map[string]time.Time

	desiredRevision int64
	lastRequestID   string
	lastSignature   string

	outbound   map[string]*downlink
	everTracks bool
	retrySkip  map[string]time.Time

	needOffer    bool
	pendingOffer bool
	offerSentAt  time.Time
	offerRetries int
	retryAt      time.Time

	connectedOnce  bool
	disconnectedAt time.Time
	iceRestarts    int
	restartAt      time.Time
	restarting     bool

	pubKeys   int
	lastPrune time.Time
}

type publishedTrack struct {
	key           string
	room          *Room
	owner         *RTCPeer
	remote        *webrtc.TrackRemote
	info          protocol.RTCTrack
	video         bool
	keyframeProbe *keyframeProbe
	detectable    bool
	closed        atomic.Bool
	lastPLI       atomic.Int64
	rtcpMu        sync.Mutex

	subsMu   sync.Mutex
	subs     map[*RTCPeer]*downlink
	subsView atomic.Pointer[[]*downlink]
}

func newWebRTCAPI(cfg WebRTCConfig) (*webrtc.API, error) {
	engine := &webrtc.MediaEngine{}
	if err := engine.RegisterDefaultCodecs(); err != nil {
		return nil, fmt.Errorf("注册 WebRTC 编解码能力失败: %w", err)
	}
	settings := webrtc.SettingEngine{}
	if err := settings.SetEphemeralUDPPortRange(uint16(cfg.UDPPortMin), uint16(cfg.UDPPortMax)); err != nil {
		return nil, fmt.Errorf("设置 WebRTC UDP 端口范围失败: %w", err)
	}
	if len(cfg.PublicIPs) > 0 {
		if err := settings.SetICEAddressRewriteRules(webrtc.ICEAddressRewriteRule{
			External:        cfg.PublicIPs,
			AsCandidateType: webrtc.ICECandidateTypeHost,
		}); err != nil {
			return nil, fmt.Errorf("设置 WebRTC 公网地址失败: %w", err)
		}
	}
	return webrtc.NewAPI(webrtc.WithMediaEngine(engine), webrtc.WithSettingEngine(settings)), nil
}

func newRTCPeer(h *Hub, room *Room, ws *websocket.Conn, decision Decision, remoteAddr, userAgent string) (*RTCPeer, error) {
	pc, err := h.rtcAPI.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		return nil, err
	}
	p := &RTCPeer{
		ID:         newID("p_", 6),
		Subject:    decision.Subject,
		RemoteAddr: remoteAddr,
		UserAgent:  userAgent,
		JoinedAt:   time.Now().UTC(),
		hub:        h,
		room:       room,
		ws:         ws,
		pc:         pc,
		out:        make(chan []byte, peerQueueSize),
		cmds:       make(chan peerCommand, peerCommandQueue),
		done:       make(chan struct{}),
	}
	p.state = peerState{
		all:       true,
		wanted:    make(map[string]struct{}),
		desiredAt: make(map[string]time.Time),
		outbound:  make(map[string]*downlink),
		retrySkip: make(map[string]time.Time),
	}
	p.snap.Store(&protocol.SubscriptionState{All: true, TrackIDs: []string{}})

	pc.OnICECandidate(func(candidate *webrtc.ICECandidate) {
		if candidate == nil || p.isClosed() {
			return
		}
		init := candidate.ToJSON()
		p.sendSignal(protocol.RTCSignal{
			Type: protocol.TypeICE,
			Candidate: &protocol.ICECandidate{
				Candidate:        init.Candidate,
				SDPMid:           init.SDPMid,
				SDPMLineIndex:    init.SDPMLineIndex,
				UsernameFragment: init.UsernameFragment,
			},
		})
	})
	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		switch state {
		case webrtc.PeerConnectionStateClosed:
			if !p.isClosed() {
				go p.finish("connection_closed")
			}
		default:
			p.postReliable(peerCommand{kind: cmdConnectionState, state: state})
		}
	})
	pc.OnTrack(func(track *webrtc.TrackRemote, receiver *webrtc.RTPReceiver) {
		p.handleTrack(track, receiver)
	})
	return p, nil
}

func (h *Hub) ServeRTCPeer(w http.ResponseWriter, r *http.Request, room *Room, decision Decision, remoteAddr string) *Error {
	if h.rtcAPI == nil {
		return ErrWebRTCUnavailable
	}
	h.mu.RLock()
	closed := h.closed
	h.mu.RUnlock()
	if closed {
		return ErrHubClosed
	}
	if room.isClosed() {
		return ErrRoomClosed
	}
	if room.isFull() {
		return ErrRoomFull
	}

	headers := http.Header{}
	headers.Set("X-Repeater-Room", room.ID)
	headers.Set("X-Repeater-Subject", url.QueryEscape(decision.Subject))
	headers.Set("X-Repeater-Transport", protocol.TransportWebRTC)

	upgrader := websocket.Upgrader{
		HandshakeTimeout: 10 * time.Second,
		ReadBufferSize:   4096,
		WriteBufferSize:  4096,
		CheckOrigin:      func(*http.Request) bool { return true },
	}
	ws, err := upgrader.Upgrade(w, r, headers)
	if err != nil {
		h.log.Debug("WebRTC 信令升级失败", "error", err, "room_id", room.ID)
		return nil
	}

	p, err := newRTCPeer(h, room, ws, decision, remoteAddr, r.UserAgent())
	if err != nil {
		h.log.Warn("创建 WebRTC 连接失败", "error", err, "room_id", room.ID)
		_ = ws.Close()
		return nil
	}
	if joinErr := room.addPeer(p); joinErr != nil {
		p.closeWith(4004, joinErr.Code)
		return nil
	}

	h.joinsTotal.Add(1)
	h.events.add("user_joined",
		fmt.Sprintf("%s 以 WebRTC 加入了房间「%s」", p.Subject, room.Name), room.ID, p.Subject)
	h.notifyAgent(room.KeyID, protocol.UserJoined{
		Type:       protocol.TypeUserJoined,
		RoomID:     room.ID,
		RoomName:   room.Name,
		ConnID:     p.ID,
		Transport:  protocol.TransportWebRTC,
		Subject:    p.Subject,
		RemoteAddr: p.RemoteAddr,
		JoinedAt:   p.JoinedAt,
	})
	h.log.Info("WebRTC 参与者加入房间",
		"room_id", room.ID, "peer_id", p.ID, "subject", p.Subject, "remote_addr", p.RemoteAddr)

	p.run()

	h.log.Info("WebRTC 参与者离开房间",
		"room_id", room.ID, "peer_id", p.ID, "subject", p.Subject, "reason", p.finishReason())
	return nil
}

func (h *Hub) removePeer(p *RTCPeer, reason string) {
	if !p.room.removePeer(p.ID) {
		return
	}
	p.removeOwnedTracks(reason)
	for _, pub := range p.room.publishedSnapshot() {
		pub.removeSubscriber(p)
	}
	p.downlinks.Store(0)
	h.events.add("user_left",
		fmt.Sprintf("%s 离开了房间「%s」", p.Subject, p.room.Name), p.room.ID, p.Subject)
	h.notifyAgent(p.room.KeyID, protocol.UserLeft{
		Type:      protocol.TypeUserLeft,
		RoomID:    p.room.ID,
		RoomName:  p.room.Name,
		ConnID:    p.ID,
		Transport: protocol.TransportWebRTC,
		Subject:   p.Subject,
		Reason:    reason,
		LeftAt:    time.Now().UTC(),
	})
}

func (p *RTCPeer) run() {
	go p.writeLoop()
	go p.actorLoop()
	p.readLoop()
	p.finish("signaling_closed")
}

func (p *RTCPeer) readLoop() {
	p.ws.SetReadLimit(peerReadLimit)
	_ = p.ws.SetReadDeadline(time.Now().Add(peerReadTimeout))
	p.ws.SetPongHandler(func(string) error {
		return p.ws.SetReadDeadline(time.Now().Add(peerReadTimeout))
	})

	for {
		messageType, raw, err := p.ws.ReadMessage()
		if err != nil {
			p.finish(readErrorReason(err))
			return
		}
		if messageType != websocket.TextMessage {
			continue
		}
		_ = p.ws.SetReadDeadline(time.Now().Add(peerReadTimeout))

		var msg protocol.RTCSignal
		if err := json.Unmarshal(raw, &msg); err != nil {
			p.sendSignal(protocol.RTCSignal{Type: protocol.TypeError, Error: "信令消息不是有效 JSON"})
			continue
		}
		switch msg.Type {
		case protocol.TypeOffer:
			if err := p.handleOfferSDP(msg.SDP); err != nil {
				p.hub.log.Warn("处理 WebRTC offer 失败",
					"error", err, "room_id", p.room.ID, "peer_id", p.ID)
				if errors.Is(err, errPeerClosed) {
					return
				}
				p.sendSignal(protocol.RTCSignal{Type: protocol.TypeError, Error: "SDP offer 无效"})
				p.finish("sdp_error")
				return
			}
		case protocol.TypeAnswer:
			if err := p.handleAnswerSDP(msg.SDP); err != nil {
				p.hub.log.Warn("处理 WebRTC answer 失败",
					"error", err, "room_id", p.room.ID, "peer_id", p.ID)
			}
		case protocol.TypeICE:
			if msg.Candidate != nil {
				p.handleICE(*msg.Candidate)
			}
		case protocol.TypeBye:
			p.finish("client_closed")
			return
		case protocol.TypeRequestKeyframe:
			if msg.Track != nil {
				p.requestOutboundKeyframe(msg.Track.ID)
			}
		case protocol.TypePing:
			p.sendSignal(protocol.RTCSignal{Type: protocol.TypePong})
		default:
			p.hub.log.Debug("忽略未知 WebRTC 信令", "type", msg.Type, "peer_id", p.ID)
		}
	}
}

func (p *RTCPeer) writeLoop() {
	ticker := time.NewTicker(peerPingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-p.done:
			return
		case raw := <-p.out:
			_ = p.ws.SetWriteDeadline(time.Now().Add(peerWriteTimeout))
			if err := p.ws.WriteMessage(websocket.TextMessage, raw); err != nil {
				p.closeWith(websocket.CloseGoingAway, "write failed")
				return
			}
		case <-ticker.C:
			if err := p.ws.WriteControl(websocket.PingMessage, nil, time.Now().Add(peerWriteTimeout)); err != nil {
				p.closeWith(websocket.CloseGoingAway, "ping failed")
				return
			}
		}
	}
}

func (p *RTCPeer) sendSignal(msg protocol.RTCSignal) bool {
	raw, err := json.Marshal(msg)
	if err != nil {
		p.hub.log.Error("序列化 WebRTC 信令失败", "error", err, "peer_id", p.ID)
		return false
	}
	select {
	case <-p.done:
		return false
	default:
	}
	select {
	case p.out <- raw:
		return true
	case <-p.done:
		return false
	default:
		p.hub.log.Warn("WebRTC 信令发送队列已满，断开连接",
			"room_id", p.room.ID, "peer_id", p.ID)
		p.closeWith(websocket.ClosePolicyViolation, "signaling queue overflow")
		return false
	}
}

func (p *RTCPeer) post(cmd peerCommand) bool {
	if p.isClosed() {
		return false
	}
	select {
	case p.cmds <- cmd:
		return true
	case <-p.done:
		return false
	}
}

func (p *RTCPeer) postAsync(cmd peerCommand) bool {
	if p.isClosed() {
		return false
	}
	select {
	case p.cmds <- cmd:
		return true
	default:
		p.resync.Store(true)
		return false
	}
}

// postReliable must be used for commands that may be produced from inside a
// Pion callback (where blocking could deadlock the PeerConnection) but must
// still be delivered eventually.
func (p *RTCPeer) postReliable(cmd peerCommand) {
	if p.postAsync(cmd) {
		return
	}
	go p.post(cmd)
}

func (p *RTCPeer) handleOfferSDP(sdp string) error {
	if strings.TrimSpace(sdp) == "" {
		return fmt.Errorf("empty offer")
	}
	reply := make(chan error, 1)
	if !p.post(peerCommand{kind: cmdOffer, sdp: sdp, reply: reply}) {
		return errPeerClosed
	}
	select {
	case err := <-reply:
		return err
	case <-p.done:
		return errPeerClosed
	case <-time.After(peerCommandWait):
		return fmt.Errorf("等待 offer 处理超时")
	}
}

func (p *RTCPeer) handleAnswerSDP(sdp string) error {
	if strings.TrimSpace(sdp) == "" {
		return fmt.Errorf("empty answer")
	}
	reply := make(chan error, 1)
	if !p.post(peerCommand{kind: cmdAnswer, sdp: sdp, reply: reply}) {
		return errPeerClosed
	}
	select {
	case err := <-reply:
		return err
	case <-p.done:
		return errPeerClosed
	case <-time.After(peerCommandWait):
		return fmt.Errorf("等待 answer 处理超时")
	}
}

func (p *RTCPeer) handleICE(candidate protocol.ICECandidate) {
	if candidate.Candidate == "" {
		return
	}
	init := webrtc.ICECandidateInit{
		Candidate:        candidate.Candidate,
		SDPMid:           candidate.SDPMid,
		SDPMLineIndex:    candidate.SDPMLineIndex,
		UsernameFragment: candidate.UsernameFragment,
	}
	p.post(peerCommand{kind: cmdICE, candidate: &init})
}

func (p *RTCPeer) handleTrack(remote *webrtc.TrackRemote, receiver *webrtc.RTPReceiver) {
	if p.isClosed() {
		return
	}
	p.postReliable(peerCommand{kind: cmdPublish, remote: remote, receiver: receiver})
}

func (p *RTCPeer) requestOutboundKeyframe(key string) {
	if key == "" {
		return
	}
	p.postReliable(peerCommand{kind: cmdRequestKeyframe, key: key})
}

func (p *RTCPeer) attachPublishedAsync(pub *publishedTrack) {
	if pub == nil {
		return
	}
	p.postAsync(peerCommand{kind: cmdTrackAdded, track: pub})
}

func (p *RTCPeer) removePublishedAsync(key string) {
	if key == "" {
		return
	}
	p.postAsync(peerCommand{kind: cmdTrackRemoved, key: key})
}

func (p *RTCPeer) reconcileAsync() {
	p.postAsync(peerCommand{kind: cmdReconcile})
}

// applySubscriptions replaces the participant's complete desired subscription
// state and reports what is attached now plus what is still pending. The relay
// only executes the consumer's list; it never derives subscriptions from track
// kind, publisher, or any other business meaning.
func (p *RTCPeer) applySubscriptions(all bool, trackIDs []string, requestID string, revision int64) (subscriptionApply, bool) {
	reply := make(chan subscriptionApply, 1)
	if !p.post(peerCommand{
		kind:      cmdSubscriptions,
		ids:       trackIDs,
		all:       all,
		requestID: requestID,
		revision:  revision,
		replySub:  reply,
	}) {
		return subscriptionApply{}, false
	}
	select {
	case res := <-reply:
		return res, res.ok
	case <-p.done:
		return subscriptionApply{}, false
	case <-time.After(peerCommandWait):
		return subscriptionApply{}, false
	}
}

func (p *RTCPeer) subscriptionState() *protocol.SubscriptionState {
	if snap := p.snap.Load(); snap != nil {
		return snap
	}
	return nil
}

func (p *RTCPeer) subscriptionCount() int {
	return int(p.downlinks.Load())
}

func (p *RTCPeer) publishedCount() int {
	return int(p.published.Load())
}

func (p *RTCPeer) isClosed() bool { return p.closed.Load() }

func (p *RTCPeer) finish(reason string) {
	p.finishOnce.Do(func() {
		p.reasonMu.Lock()
		p.reason = reason
		p.reasonMu.Unlock()
		p.closed.Store(true)
		p.closeWith(websocket.CloseNormalClosure, "closed")
		p.hub.removePeer(p, reason)
	})
}

func (p *RTCPeer) finishReason() string {
	p.reasonMu.Lock()
	defer p.reasonMu.Unlock()
	return p.reason
}

func (p *RTCPeer) closeWith(code int, reason string) {
	p.closeOnce.Do(func() {
		p.closed.Store(true)
		close(p.done)
		_ = p.pc.Close()
		_ = p.ws.WriteControl(
			websocket.CloseMessage,
			websocket.FormatCloseMessage(code, reason),
			time.Now().Add(peerCloseTimeout),
		)
		_ = p.ws.Close()
	})
}

func (p *RTCPeer) removeOwnedTracks(reason string) {
	for _, pub := range p.room.publishedSnapshot() {
		if pub.owner == p {
			p.hub.removePublishedTrack(pub, reason)
		}
	}
}

func (p *RTCPeer) actorLoop() {
	ticker := time.NewTicker(peerTickInterval)
	defer ticker.Stop()
	defer p.teardown()
	for {
		if p.isClosed() {
			return
		}
		select {
		case <-p.done:
			return
		case cmd := <-p.cmds:
			p.handleCommand(cmd)
		case now := <-ticker.C:
			p.onTick(now)
		}
	}
}

func (p *RTCPeer) handleCommand(cmd peerCommand) {
	switch cmd.kind {
	case cmdOffer:
		err := p.onOffer(cmd.sdp)
		replyErr(cmd.reply, err)
	case cmdAnswer:
		err := p.onAnswer(cmd.sdp)
		replyErr(cmd.reply, err)
	case cmdICE:
		p.onICE(cmd.candidate)
	case cmdPublish:
		p.onPublish(cmd.remote, cmd.receiver)
	case cmdTrackAdded:
		if cmd.track != nil && !cmd.track.closed.Load() && p.wantsKey(cmd.track.key) {
			p.reconcile()
		}
	case cmdTrackRemoved:
		p.onTrackRemoved(cmd.key)
	case cmdTrackReleased:
		if p.state.pubKeys > 0 {
			p.state.pubKeys--
		}
	case cmdDownlinkFailed:
		p.state.retrySkip[cmd.key] = time.Now().Add(downlinkRetryDelay)
		if p.detachLocked(cmd.key) {
			p.requestNegotiation()
		}
	case cmdReconcile:
		p.reconcile()
	case cmdSubscriptions:
		res := p.onSubscriptions(cmd)
		replySub(cmd.replySub, res)
	case cmdRequestKeyframe:
		if dl := p.state.outbound[cmd.key]; dl != nil && dl.video {
			dl.markKeyframeNeeded()
			dl.pliDue = time.Now()
			dl.pliBackoff = 0
		}
	case cmdConnectionState:
		p.onConnectionState(cmd.state)
	}
}

func replyErr(ch chan error, err error) {
	if ch == nil {
		return
	}
	select {
	case ch <- err:
	default:
	}
}

func replySub(ch chan subscriptionApply, res subscriptionApply) {
	if ch == nil {
		return
	}
	select {
	case ch <- res:
	default:
	}
}

func (p *RTCPeer) onConnectionState(state webrtc.PeerConnectionState) {
	if p.isClosed() {
		return
	}
	switch state {
	case webrtc.PeerConnectionStateConnected:
		p.state.connectedOnce = true
		p.state.disconnectedAt = time.Time{}
		p.state.restartAt = time.Time{}
		p.state.iceRestarts = 0
		p.state.restarting = false
		p.state.retryAt = time.Time{}
	case webrtc.PeerConnectionStateDisconnected:
		if p.state.disconnectedAt.IsZero() {
			p.state.disconnectedAt = time.Now()
		}
	case webrtc.PeerConnectionStateFailed:
		if p.state.disconnectedAt.IsZero() {
			p.state.disconnectedAt = time.Now()
		}
	}
}

func (p *RTCPeer) onOffer(sdp string) error {
	if p.isClosed() {
		return nil
	}
	if sdp == "" {
		return fmt.Errorf("empty offer")
	}
	st := &p.state
	resume := false
	if p.pc.SignalingState() == webrtc.SignalingStateHaveLocalOffer {
		if err := p.pc.SetLocalDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeRollback}); err != nil {
			return fmt.Errorf("rollback local offer: %w", err)
		}
		st.needOffer = true
		st.offerRetries = 0
		resume = true
	}
	st.pendingOffer = false
	if state := p.pc.SignalingState(); state != webrtc.SignalingStateStable {
		return fmt.Errorf("unexpected signaling state %s", state)
	}
	if err := p.pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: sdp}); err != nil {
		return fmt.Errorf("set remote offer: %w", err)
	}
	st.remoteSet = true
	p.flushICE()

	answer, err := p.pc.CreateAnswer(nil)
	if err != nil {
		return fmt.Errorf("create answer: %w", err)
	}
	if err := p.pc.SetLocalDescription(answer); err != nil {
		return fmt.Errorf("set local answer: %w", err)
	}
	local := p.pc.LocalDescription()
	if local == nil {
		return fmt.Errorf("local description missing")
	}

	first := !st.initialSynced
	st.initialSynced = true
	st.offerRetries = 0
	st.restarting = false
	st.retryAt = time.Time{}
	p.sendSignal(protocol.RTCSignal{Type: protocol.TypeAnswer, SDP: local.SDP})

	if first {
		p.reconcile()
	} else if resume {
		p.requestNegotiation()
	}
	return nil
}

func (p *RTCPeer) onAnswer(sdp string) error {
	if p.isClosed() {
		return nil
	}
	if sdp == "" {
		return fmt.Errorf("empty answer")
	}
	st := &p.state
	if p.pc.SignalingState() != webrtc.SignalingStateHaveLocalOffer {
		if st.needOffer {
			p.requestNegotiation()
		}
		return fmt.Errorf("no local offer to answer")
	}
	if err := p.pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: sdp}); err != nil {
		return fmt.Errorf("set remote answer: %w", err)
	}
	st.remoteSet = true
	p.flushICE()
	st.pendingOffer = false
	st.offerRetries = 0
	st.restarting = false
	st.retryAt = time.Time{}
	if st.needOffer {
		p.requestNegotiation()
		return nil
	}
	p.kickPendingKeyframes(time.Now())
	return nil
}

func (p *RTCPeer) onICE(candidate *webrtc.ICECandidateInit) {
	if candidate == nil {
		return
	}
	if !p.state.remoteSet {
		limit := p.hub.cfg.WebRTC.PendingICELimit
		if limit <= 0 || len(p.state.pendingICE) < limit {
			p.state.pendingICE = append(p.state.pendingICE, *candidate)
		}
		return
	}
	if err := p.pc.AddICECandidate(*candidate); err != nil {
		p.hub.log.Debug("添加 ICE 候选失败",
			"error", err, "room_id", p.room.ID, "peer_id", p.ID)
	}
}

// flushICE drains candidates buffered before the remote description existed.
func (p *RTCPeer) flushICE() {
	pending := p.state.pendingICE
	p.state.pendingICE = nil
	for _, candidate := range pending {
		if err := p.pc.AddICECandidate(candidate); err != nil {
			p.hub.log.Debug("添加待处理 ICE 候选失败",
				"error", err, "room_id", p.room.ID, "peer_id", p.ID)
		}
	}
}

func (p *RTCPeer) onPublish(remote *webrtc.TrackRemote, receiver *webrtc.RTPReceiver) {
	if remote == nil || p.isClosed() || p.room.isClosed() {
		return
	}
	st := &p.state
	limit := p.hub.cfg.WebRTC.MaxTracksPerPeer
	if limit < 1 {
		limit = 1
	}
	if st.pubKeys >= limit {
		p.hub.log.Warn("WebRTC 参与者轨道数量超限",
			"room_id", p.room.ID, "peer_id", p.ID, "limit", limit)
		p.sendSignal(protocol.RTCSignal{Type: protocol.TypeError, Error: "发布轨道数量超过上限"})
		return
	}
	key := newID("t_", 8)
	codec := remote.Codec()
	info := protocol.RTCTrack{
		ID:          key,
		Kind:        remote.Kind().String(),
		StreamID:    remote.StreamID(),
		Codec:       codec.MimeType,
		RID:         remote.RID(),
		PublisherID: p.ID,
		Subject:     p.Subject,
	}
	probe := newKeyframeProbe(codec.MimeType)
	pub := &publishedTrack{
		key:           key,
		room:          p.room,
		owner:         p,
		remote:        remote,
		info:          info,
		video:         info.Kind == "video",
		keyframeProbe: probe,
		detectable:    probe.detectable(),
		subs:          make(map[*RTCPeer]*downlink),
	}
	if !p.room.publish(pub) {
		return
	}
	st.pubKeys++
	p.published.Add(1)
	p.hub.notifyAgent(p.room.KeyID, protocol.TrackPublished{
		Type:        protocol.TypeTrackPublished,
		RoomID:      p.room.ID,
		RoomName:    p.room.Name,
		ConnID:      p.ID,
		Track:       info,
		PublishedAt: time.Now().UTC(),
	})
	go pub.readLoop()
	go drainReceiverRTCP(receiver)
	for _, peer := range p.room.peersSnapshot() {
		if peer == p {
			continue
		}
		peer.attachPublishedAsync(pub)
	}
	p.hub.log.Info("WebRTC 轨道已发布",
		"room_id", p.room.ID, "peer_id", p.ID, "track_id", key,
		"kind", info.Kind, "codec", info.Codec)
}

func (p *RTCPeer) onTrackRemoved(key string) {
	if p.isClosed() {
		return
	}
	if p.detachLocked(key) {
		p.requestNegotiation()
	}
}

func (p *RTCPeer) wantsKey(key string) bool {
	if p.state.all {
		return true
	}
	_, ok := p.state.wanted[key]
	return ok
}

func (p *RTCPeer) reconcile() {
	if p.isClosed() {
		return
	}
	st := &p.state
	if !st.initialSynced {
		p.refreshSubscriptionSnapshot()
		return
	}
	now := time.Now()
	for key, until := range st.retrySkip {
		if !now.Before(until) {
			delete(st.retrySkip, key)
		}
	}
	changed := false
	pubs := p.room.publishedSnapshot()
	live := make(map[string]*publishedTrack, len(pubs))
	for _, pub := range pubs {
		if pub.owner == p || pub.closed.Load() {
			continue
		}
		live[pub.key] = pub
		if !p.wantsKey(pub.key) {
			continue
		}
		if _, ok := st.outbound[pub.key]; ok {
			continue
		}
		if _, skip := st.retrySkip[pub.key]; skip {
			continue
		}
		if p.attachLocked(pub) {
			changed = true
		}
	}
	for key := range st.outbound {
		pub := live[key]
		if pub != nil && p.wantsKey(key) {
			continue
		}
		if p.detachLocked(key) {
			changed = true
		}
	}
	p.refreshSubscriptionSnapshot()
	if changed {
		p.requestNegotiation()
	}
}

func (p *RTCPeer) attachLocked(pub *publishedTrack) bool {
	if p.isClosed() || pub == nil || pub.closed.Load() {
		return false
	}
	if _, exists := p.state.outbound[pub.key]; exists {
		return false
	}
	codec := pub.remote.Codec().RTPCodecCapability
	local, err := webrtc.NewTrackLocalStaticRTP(codec, pub.info.ID, pub.info.StreamID)
	if err != nil {
		p.hub.log.Warn("创建转发轨道失败",
			"error", err, "room_id", p.room.ID, "peer_id", p.ID, "track_id", pub.key)
		return false
	}
	sender, err := p.pc.AddTrack(local)
	if err != nil {
		p.hub.log.Warn("添加转发轨道失败",
			"error", err, "room_id", p.room.ID, "peer_id", p.ID, "track_id", pub.key)
		return false
	}
	queue := p.hub.cfg.WebRTC.AudioQueuePackets
	if pub.video {
		queue = p.hub.cfg.WebRTC.VideoQueuePackets
	}
	dl := newDownlink(p, pub, local, sender, queue)
	if !pub.addSubscriber(p, dl) {
		_ = p.pc.RemoveTrack(sender)
		return false
	}
	p.state.outbound[pub.key] = dl
	p.state.everTracks = true
	p.downlinks.Add(1)
	if pub.video {
		dl.pliDue = time.Now()
		dl.pliBackoff = 0
	}
	info := pub.info
	p.sendSignal(protocol.RTCSignal{Type: protocol.TypeTrackAdded, Track: &info})
	go dl.run()
	go p.drainRTCP(pub, dl)
	p.hub.log.Info("WebRTC 轨道已订阅",
		"room_id", p.room.ID, "peer_id", p.ID, "track_id", pub.key,
		"publisher_id", pub.owner.ID, "kind", info.Kind)
	p.refreshSubscriptionSnapshot()
	return true
}

func (p *RTCPeer) detachLocked(key string) bool {
	dl := p.state.outbound[key]
	if dl == nil {
		return false
	}
	delete(p.state.outbound, key)
	p.downlinks.Add(-1)
	dl.close()
	dl.pub.removeSubscriber(p)
	if !p.isClosed() {
		_ = p.pc.RemoveTrack(dl.sender)
	}
	info := dl.pub.info
	p.sendSignal(protocol.RTCSignal{Type: protocol.TypeTrackRemoved, Track: &info})
	p.hub.log.Info("WebRTC 轨道已退订",
		"room_id", p.room.ID, "peer_id", p.ID, "track_id", key, "kind", info.Kind)
	p.refreshSubscriptionSnapshot()
	return true
}

func (p *RTCPeer) teardown() {
	for key, dl := range p.state.outbound {
		delete(p.state.outbound, key)
		dl.close()
		dl.pub.removeSubscriber(p)
	}
	p.downlinks.Store(0)
}

func (p *RTCPeer) requestNegotiation() {
	if p.isClosed() {
		return
	}
	p.state.needOffer = true
	p.tryNegotiate(time.Now())
}

func (p *RTCPeer) tryNegotiate(now time.Time) {
	st := &p.state
	if p.isClosed() || !st.initialSynced || !st.needOffer || st.pendingOffer {
		return
	}
	if now.Before(st.retryAt) {
		return
	}
	if state := p.pc.SignalingState(); state != webrtc.SignalingStateStable {
		st.retryAt = now.Add(offerRetryBase)
		return
	}
	if len(st.outbound) == 0 && !st.everTracks && !st.restarting {
		st.needOffer = false
		return
	}
	options := &webrtc.OfferOptions{}
	if st.restarting {
		options.ICERestart = true
	}
	offer, err := p.pc.CreateOffer(options)
	if err != nil {
		p.negotiationFailed("create_offer", err)
		return
	}
	if err := p.pc.SetLocalDescription(offer); err != nil {
		p.negotiationFailed("set_offer", err)
		return
	}
	local := p.pc.LocalDescription()
	if local == nil {
		p.negotiationFailed("local_description", errors.New("local description missing"))
		return
	}
	st.needOffer = false
	st.pendingOffer = true
	st.offerSentAt = now
	p.sendSignal(protocol.RTCSignal{Type: protocol.TypeOffer, SDP: local.SDP})
}

func (p *RTCPeer) negotiationFailed(stage string, err error) {
	p.hub.negotiationFailures.Add(1)
	p.hub.log.Warn("WebRTC 协商失败",
		"stage", stage, "error", err, "room_id", p.room.ID, "peer_id", p.ID)
	p.finish("sdp_error")
}

func (p *RTCPeer) onTick(now time.Time) {
	p.tickOffer(now)
	p.tickConnect(now)
	if p.isClosed() {
		return
	}
	p.tickConnection(now)
	p.tickKeyframes(now)
	if now.Sub(p.state.lastPrune) >= peerDesiredPruneGap {
		p.state.lastPrune = now
		p.pruneDesired(now)
	}
	if p.expireRetrySkips(now) {
		p.reconcile()
	}
	if p.resync.Swap(false) {
		p.reconcile()
	}
}

func (p *RTCPeer) expireRetrySkips(now time.Time) bool {
	expired := false
	for key, until := range p.state.retrySkip {
		if now.Before(until) {
			continue
		}
		delete(p.state.retrySkip, key)
		expired = true
	}
	return expired
}

// tickOffer rolls back an unanswered offer and retries with backoff instead of
// tearing down the session; backgrounded clients resume answering on wake.
func (p *RTCPeer) tickOffer(now time.Time) {
	st := &p.state
	if st.pendingOffer {
		if now.Sub(st.offerSentAt) < peerOfferTimeout {
			return
		}
		st.pendingOffer = false
		st.needOffer = true
		st.offerRetries++
		if p.pc.SignalingState() == webrtc.SignalingStateHaveLocalOffer {
			if err := p.pc.SetLocalDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeRollback}); err != nil {
				p.negotiationFailed("rollback_offer", err)
				return
			}
		}
		backoff := offerRetryDelay(st.offerRetries)
		st.retryAt = now.Add(backoff)
		if st.restarting {
			st.restarting = false
			st.needOffer = false
			if st.disconnectedAt.IsZero() {
				st.disconnectedAt = now
			}
		}
		p.hub.log.Warn("WebRTC 重协商超时，保留连接",
			"room_id", p.room.ID, "peer_id", p.ID,
			"retry", st.offerRetries, "retry_in", backoff.String())
		return
	}
	if st.needOffer && st.disconnectedAt.IsZero() {
		p.tryNegotiate(now)
	}
}

func offerRetryDelay(attempt int) time.Duration {
	delay := offerRetryBase
	for i := 1; i < attempt && delay < offerRetryMax; i++ {
		delay *= 2
	}
	if delay > offerRetryMax {
		delay = offerRetryMax
	}
	return delay
}

// tickConnect reclaims peers that never establish ICE; without this a client
// stuck in connecting keeps a room slot until it closes its own socket.
func (p *RTCPeer) tickConnect(now time.Time) {
	if p.state.connectedOnce {
		return
	}
	if p.pc.ConnectionState() == webrtc.PeerConnectionStateConnected {
		p.state.connectedOnce = true
		return
	}
	timeout := p.hub.cfg.WebRTC.ConnectTimeout
	if timeout <= 0 || now.Sub(p.JoinedAt) < timeout {
		return
	}
	p.hub.log.Warn("WebRTC 首次连接超时，关闭连接",
		"room_id", p.room.ID, "peer_id", p.ID, "elapsed", now.Sub(p.JoinedAt).String())
	p.finish("connect_timeout")
}

func (p *RTCPeer) tickConnection(now time.Time) {
	st := &p.state
	if st.disconnectedAt.IsZero() {
		return
	}
	elapsed := now.Sub(st.disconnectedAt)
	if elapsed >= iceFailureTTL {
		p.hub.log.Warn("WebRTC ICE 恢复超过时限，关闭连接",
			"room_id", p.room.ID, "peer_id", p.ID,
			"restarts", st.iceRestarts, "elapsed", elapsed.String())
		p.finish("ice_failed")
		return
	}
	if elapsed < iceDisconnectGrace {
		return
	}
	if st.restarting {
		if !st.pendingOffer {
			p.tryNegotiate(now)
		}
		return
	}
	if !st.restartAt.IsZero() && now.Sub(st.restartAt) < iceRestartDelay(st.iceRestarts) {
		return
	}
	p.startICERestart(now)
}

func iceRestartDelay(attempt int) time.Duration {
	delay := iceRestartBase
	for i := 1; i < attempt && delay < iceRestartMax; i++ {
		delay *= 2
	}
	if delay > iceRestartMax {
		delay = iceRestartMax
	}
	return delay
}

func (p *RTCPeer) startICERestart(now time.Time) {
	st := &p.state
	st.iceRestarts++
	st.restartAt = now
	p.hub.iceRestarts.Add(1)

	if p.pc.SignalingState() == webrtc.SignalingStateHaveLocalOffer {
		if err := p.pc.SetLocalDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeRollback}); err != nil {
			p.negotiationFailed("ice_restart_rollback", err)
			return
		}
		st.pendingOffer = false
	}
	st.restarting = true
	st.needOffer = true
	st.retryAt = now
	p.hub.log.Warn("WebRTC 连接中断，尝试 ICE 重启",
		"room_id", p.room.ID, "peer_id", p.ID, "attempt", st.iceRestarts)
	p.tryNegotiate(now)
}

func (p *RTCPeer) tickKeyframes(now time.Time) {
	for _, dl := range p.state.outbound {
		if !dl.video {
			continue
		}
		if dl.pliReset.Swap(false) {
			dl.pliBackoff = 0
			dl.pliDue = now
		}
		if !dl.waiting.Load() || now.Before(dl.pliDue) {
			continue
		}
		dl.pub.requestKeyframe()
		p.hub.keyframeRequests.Add(1)
		dl.pliIssued.Store(true)
		if dl.pliBackoff == 0 {
			dl.pliBackoff = keyframeBackoff
		} else {
			dl.pliBackoff *= 2
			if dl.pliBackoff > keyframeMaxDelay {
				dl.pliBackoff = keyframeMaxDelay
			}
		}
		dl.pliDue = now.Add(dl.pliBackoff)
	}
}

func (p *RTCPeer) kickPendingKeyframes(now time.Time) {
	for _, dl := range p.state.outbound {
		if dl.video && dl.waiting.Load() {
			dl.pliDue = now
			dl.pliBackoff = 0
		}
	}
}

func (p *RTCPeer) pruneDesired(now time.Time) {
	st := &p.state
	if st.all || len(st.desiredAt) == 0 {
		return
	}
	published := make(map[string]struct{})
	for _, pub := range p.room.publishedSnapshot() {
		published[pub.key] = struct{}{}
	}
	ttl := p.hub.cfg.WebRTC.DesiredTTL
	removed := 0
	for id, at := range st.desiredAt {
		if _, ok := published[id]; ok {
			continue
		}
		if now.Sub(at) < ttl {
			continue
		}
		delete(st.desiredAt, id)
		delete(st.wanted, id)
		removed++
	}
	if removed == 0 {
		return
	}
	kept := st.ids[:0]
	for _, id := range st.ids {
		if _, ok := st.wanted[id]; ok {
			kept = append(kept, id)
		}
	}
	st.ids = kept
	p.refreshSubscriptionSnapshot()
	p.hub.log.Info("清理未出现的订阅轨道",
		"room_id", p.room.ID, "peer_id", p.ID, "removed", removed, "remaining", len(st.ids))
}

func (p *RTCPeer) onSubscriptions(cmd peerCommand) subscriptionApply {
	st := &p.state
	if p.isClosed() {
		return subscriptionApply{}
	}
	limit := p.hub.cfg.WebRTC.MaxDesiredTracks
	ids := normalizeTrackIDs(cmd.ids, limit)
	if len(cmd.ids) > limit {
		p.hub.log.Warn("订阅列表超过上限，已截断",
			"room_id", p.room.ID, "peer_id", p.ID, "limit", limit)
	}
	sig := subscriptionSignature(cmd.requestID, cmd.all, ids)
	if cmd.requestID != "" && cmd.requestID == st.lastRequestID && sig == st.lastSignature {
		res := p.subscriptionResult()
		res.duplicate = true
		return res
	}
	if cmd.revision > 0 && st.desiredRevision > 0 && cmd.revision < st.desiredRevision {
		res := p.subscriptionResult()
		res.stale = true
		return res
	}

	st.all = cmd.all
	st.wanted = make(map[string]struct{}, len(ids))
	st.desiredAt = make(map[string]time.Time, len(ids))
	if !cmd.all {
		now := time.Now()
		for _, id := range ids {
			st.wanted[id] = struct{}{}
			st.desiredAt[id] = now
		}
	}
	st.ids = ids
	if cmd.revision > st.desiredRevision {
		st.desiredRevision = cmd.revision
	}
	st.lastRequestID = cmd.requestID
	st.lastSignature = sig

	p.reconcile()
	res := p.subscriptionResult()
	p.hub.log.Info("WebRTC 订阅已更新",
		"room_id", p.room.ID, "peer_id", p.ID, "request_id", cmd.requestID,
		"revision", st.desiredRevision, "all", st.all, "desired", len(ids),
		"applied", len(res.applied), "pending", len(res.pending))
	return res
}

func (p *RTCPeer) subscriptionResult() subscriptionApply {
	st := &p.state
	res := subscriptionApply{
		all:      st.all,
		revision: st.desiredRevision,
		applied:  st.appliedIDs(),
		pending:  st.pendingIDs(),
		ok:       true,
	}
	if st.all {
		res.normalized = []string{}
	} else {
		res.normalized = append([]string{}, st.ids...)
	}
	if res.applied == nil {
		res.applied = []string{}
	}
	if res.pending == nil {
		res.pending = []string{}
	}
	return res
}

func (st *peerState) appliedIDs() []string {
	out := make([]string, 0, len(st.outbound))
	for key := range st.outbound {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

func (st *peerState) pendingIDs() []string {
	if st.all {
		return nil
	}
	out := make([]string, 0, len(st.ids))
	for _, id := range st.ids {
		if _, ok := st.outbound[id]; !ok {
			out = append(out, id)
		}
	}
	return out
}

func (p *RTCPeer) refreshSubscriptionSnapshot() {
	st := &p.state
	snap := &protocol.SubscriptionState{
		All:       st.all,
		TrackIDs:  append([]string{}, st.ids...),
		Revision:  st.desiredRevision,
		Applied:   st.appliedIDs(),
		UpdatedAt: time.Now().UTC(),
	}
	p.snap.Store(snap)
}

func normalizeTrackIDs(trackIDs []string, limit int) []string {
	seen := make(map[string]struct{}, len(trackIDs))
	out := make([]string, 0, len(trackIDs))
	for _, id := range trackIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
		if len(out) >= limit {
			break
		}
	}
	return out
}

func subscriptionSignature(requestID string, all bool, ids []string) string {
	return fmt.Sprintf("%s|%t|%s", requestID, all, strings.Join(ids, "\x00"))
}

func (p *RTCPeer) drainRTCP(pub *publishedTrack, dl *downlink) {
	for {
		packets, _, err := dl.sender.ReadRTCP()
		if err != nil {
			return
		}
		for _, packet := range packets {
			switch packet.(type) {
			case *rtcp.PictureLossIndication, *rtcp.FullIntraRequest:
				dl.markKeyframeNeeded()
				pub.requestKeyframe()
				p.hub.keyframeRequests.Add(1)
			}
		}
	}
}

func drainReceiverRTCP(receiver *webrtc.RTPReceiver) {
	if receiver == nil {
		return
	}
	for {
		if _, _, err := receiver.ReadRTCP(); err != nil {
			return
		}
	}
}

func (h *Hub) removePublishedTrack(t *publishedTrack, reason string) {
	if t == nil || !t.closed.CompareAndSwap(false, true) {
		return
	}
	t.room.unpublish(t.key)
	t.owner.published.Add(-1)
	t.owner.postReliable(peerCommand{kind: cmdTrackReleased, key: t.key})
	if !t.room.isClosed() {
		h.notifyAgent(t.room.KeyID, protocol.TrackUnpublished{
			Type:          protocol.TypeTrackUnpublished,
			RoomID:        t.room.ID,
			RoomName:      t.room.Name,
			ConnID:        t.owner.ID,
			Track:         t.info,
			Reason:        reason,
			UnpublishedAt: time.Now().UTC(),
		})
	}
	t.subsMu.Lock()
	peers := make([]*RTCPeer, 0, len(t.subs))
	for peer := range t.subs {
		peers = append(peers, peer)
	}
	t.subs = make(map[*RTCPeer]*downlink)
	t.rebuildSubsLocked()
	t.subsMu.Unlock()
	for _, peer := range peers {
		peer.removePublishedAsync(t.key)
	}
	h.log.Info("WebRTC 轨道已移除",
		"room_id", t.room.ID, "peer_id", t.owner.ID, "track_id", t.key, "reason", reason)
}

func (t *publishedTrack) addSubscriber(peer *RTCPeer, dl *downlink) bool {
	t.subsMu.Lock()
	defer t.subsMu.Unlock()
	if t.closed.Load() || peer.isClosed() {
		return false
	}
	if _, exists := t.subs[peer]; exists {
		return false
	}
	t.subs[peer] = dl
	t.rebuildSubsLocked()
	return true
}

func (t *publishedTrack) removeSubscriber(peer *RTCPeer) {
	t.subsMu.Lock()
	defer t.subsMu.Unlock()
	if _, exists := t.subs[peer]; !exists {
		return
	}
	delete(t.subs, peer)
	t.rebuildSubsLocked()
}

func (t *publishedTrack) rebuildSubsLocked() {
	out := make([]*downlink, 0, len(t.subs))
	for _, dl := range t.subs {
		out = append(out, dl)
	}
	t.subsView.Store(&out)
}

// subsSnapshot returns an immutable view published by subscription updates,
// so the RTP read loop never takes the subscriber mutex or allocates.
func (t *publishedTrack) subsSnapshot() []*downlink {
	view := t.subsView.Load()
	if view == nil {
		return nil
	}
	return *view
}

func (t *publishedTrack) requestKeyframe() {
	now := time.Now().UnixMilli()
	last := t.lastPLI.Load()
	if now-last < keyframeInterval.Milliseconds() {
		return
	}
	if !t.lastPLI.CompareAndSwap(last, now) {
		return
	}
	t.writeKeyframeRequest()
}

func (t *publishedTrack) writeKeyframeRequest() {
	if t.closed.Load() || t.owner.isClosed() {
		return
	}
	t.rtcpMu.Lock()
	defer t.rtcpMu.Unlock()
	if err := t.owner.pc.WriteRTCP([]rtcp.Packet{
		&rtcp.PictureLossIndication{MediaSSRC: uint32(t.remote.SSRC())},
	}); err != nil {
		t.owner.hub.log.Debug("发送关键帧请求失败",
			"room_id", t.room.ID, "track_id", t.key, "error", err)
	}
}

func (t *publishedTrack) readLoop() {
	buffer := make([]byte, 8192)
	var header rtp.Header
	var lastVideo time.Time
	for {
		n, _, err := t.remote.Read(buffer)
		if err != nil {
			t.owner.hub.removePublishedTrack(t, "track_ended")
			return
		}
		if n == 0 {
			continue
		}
		offset, err := header.Unmarshal(buffer[:n])
		if err != nil {
			continue
		}
		resumed := false
		if t.video {
			now := time.Now()
			if !lastVideo.IsZero() && now.Sub(lastVideo) >= keyframeGap {
				resumed = true
			}
			lastVideo = now
		}
		t.owner.room.recordRTP(int64(n))
		t.owner.hub.rtpPackets.Add(1)
		t.owner.hub.rtpBytes.Add(int64(n))

		subs := t.subsSnapshot()
		if len(subs) == 0 {
			continue
		}
		raw := make([]byte, n)
		copy(raw, buffer[:n])
		keyframe := false
		if t.video && t.detectable && offset < n {
			keyframe = t.keyframeProbe.isKeyframe(raw[offset:])
		}
		if resumed {
			t.requestKeyframe()
			for _, dl := range subs {
				dl.markKeyframeNeeded()
			}
		}
		for _, dl := range subs {
			dl.enqueue(raw)
			if keyframe {
				dl.clearKeyframeWait()
			}
		}
	}
}
