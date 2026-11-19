package hub

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
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
	peerQueueSize    = 128
	peerReadLimit    = 512 << 10
	peerReadTimeout  = 75 * time.Second
	peerPingInterval = 25 * time.Second
	peerWriteTimeout = 10 * time.Second
	peerCloseTimeout = 3 * time.Second
	peerOfferTimeout = 15 * time.Second
	keyframeInterval = 500 * time.Millisecond
	keyframeGap      = time.Second
)

// RTCPeer is a room member whose media is carried by a Pion PeerConnection.
// The signaling WebSocket only carries SDP/ICE JSON; RTP bypasses it.
type RTCPeer struct {
	ID         string
	Subject    string
	RemoteAddr string
	UserAgent  string
	JoinedAt   time.Time

	hub  *Hub
	room *Room
	ws   *websocket.Conn
	pc   *webrtc.PeerConnection

	out        chan []byte
	done       chan struct{}
	closeOnce  sync.Once
	finishOnce sync.Once
	reasonMu   sync.Mutex
	reason     string
	closed     atomic.Bool

	opMu       sync.Mutex
	remoteSet  bool
	pendingICE []webrtc.ICECandidateInit
	everTracks bool

	negMu          sync.Mutex
	needOffer      bool
	negotiating    bool
	pendingOffer   bool
	initialSynced  bool
	negotiateTimer *time.Timer
	keyframeOnSync atomic.Bool

	tracksMu sync.Mutex
	outbound map[string]*outboundTrack

	subMu         sync.Mutex
	autoSubscribe bool
	wanted        map[string]struct{}

	metaMu  sync.Mutex
	pubKeys map[string]struct{}
}

type outboundTrack struct {
	pub    *publishedTrack
	local  *webrtc.TrackLocalStaticRTP
	sender *webrtc.RTPSender
}

type publishedTrack struct {
	key     string
	room    *Room
	owner   *RTCPeer
	remote  *webrtc.TrackRemote
	info    protocol.RTCTrack
	closed  atomic.Bool
	lastPLI atomic.Int64
	rtcpMu  sync.Mutex

	subsMu sync.Mutex
	subs   map[*RTCPeer]*webrtc.TrackLocalStaticRTP
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
		ID:            newID("p_", 6),
		Subject:       decision.Subject,
		RemoteAddr:    remoteAddr,
		UserAgent:     userAgent,
		JoinedAt:      time.Now().UTC(),
		hub:           h,
		room:          room,
		ws:            ws,
		pc:            pc,
		out:           make(chan []byte, peerQueueSize),
		done:          make(chan struct{}),
		outbound:      make(map[string]*outboundTrack),
		autoSubscribe: true,
		wanted:        make(map[string]struct{}),
		pubKeys:       make(map[string]struct{}),
	}

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
		case webrtc.PeerConnectionStateFailed:
			go p.finish("ice_failed")
		case webrtc.PeerConnectionStateClosed:
			if !p.isClosed() {
				go p.finish("connection_closed")
			}
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
	p.detachAllTracks(reason)
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
			if err := p.handleOffer(msg.SDP); err != nil {
				p.hub.log.Warn("处理 WebRTC offer 失败", "error", err, "peer_id", p.ID)
				p.sendSignal(protocol.RTCSignal{Type: protocol.TypeError, Error: "SDP offer 无效"})
				p.finish("sdp_error")
				return
			}
		case protocol.TypeAnswer:
			if err := p.handleAnswer(msg.SDP); err != nil {
				p.hub.log.Warn("处理 WebRTC answer 失败", "error", err, "peer_id", p.ID)
			}
		case protocol.TypeICE:
			if msg.Candidate != nil {
				p.handleICE(*msg.Candidate)
			}
		case protocol.TypeBye:
			p.finish("client_closed")
			return
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
		p.hub.log.Warn("WebRTC 信令发送队列已满，断开连接", "peer_id", p.ID)
		p.closeWith(websocket.ClosePolicyViolation, "signaling queue overflow")
		return false
	}
}

func (p *RTCPeer) handleOffer(sdp string) error {
	if sdp == "" {
		return fmt.Errorf("empty offer")
	}
	p.opMu.Lock()
	defer p.opMu.Unlock()
	if p.isClosed() {
		return nil
	}
	resume := false
	if p.pc.SignalingState() == webrtc.SignalingStateHaveLocalOffer {
		if err := p.pc.SetLocalDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeRollback}); err != nil {
			return fmt.Errorf("rollback local offer: %w", err)
		}
		p.negMu.Lock()
		p.pendingOffer = false
		p.needOffer = true
		p.stopOfferTimerLocked()
		p.negMu.Unlock()
		resume = true
	}
	if state := p.pc.SignalingState(); state != webrtc.SignalingStateStable {
		return fmt.Errorf("unexpected signaling state %s", state)
	}
	if err := p.pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: sdp}); err != nil {
		return fmt.Errorf("set remote offer: %w", err)
	}
	p.remoteSet = true
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

	p.negMu.Lock()
	first := !p.initialSynced
	p.initialSynced = true
	p.negMu.Unlock()
	p.sendSignal(protocol.RTCSignal{Type: protocol.TypeAnswer, SDP: local.SDP})

	if first {
		go p.syncPublishedTracks()
	} else if resume {
		p.requestNegotiation()
	}
	return nil
}

func (p *RTCPeer) handleAnswer(sdp string) error {
	if sdp == "" {
		return fmt.Errorf("empty answer")
	}
	p.opMu.Lock()
	defer p.opMu.Unlock()
	if p.isClosed() {
		return nil
	}
	if p.pc.SignalingState() != webrtc.SignalingStateHaveLocalOffer {
		return fmt.Errorf("no local offer to answer")
	}
	if err := p.pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: sdp}); err != nil {
		return fmt.Errorf("set remote answer: %w", err)
	}
	p.remoteSet = true
	p.flushICE()

	p.negMu.Lock()
	p.pendingOffer = false
	p.stopOfferTimerLocked()
	more := p.needOffer
	p.negMu.Unlock()
	if more {
		p.requestNegotiation()
		return nil
	}
	if p.keyframeOnSync.Swap(false) {
		p.requestOutboundKeyframes()
	}
	return nil
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
	p.opMu.Lock()
	if !p.remoteSet {
		p.pendingICE = append(p.pendingICE, init)
		p.opMu.Unlock()
		return
	}
	p.opMu.Unlock()
	if err := p.pc.AddICECandidate(init); err != nil {
		p.hub.log.Debug("添加 ICE 候选失败", "error", err, "peer_id", p.ID)
	}
}

// flushICE must be called with opMu held after a remote description is set.
func (p *RTCPeer) flushICE() {
	pending := p.pendingICE
	p.pendingICE = nil
	for _, candidate := range pending {
		if err := p.pc.AddICECandidate(candidate); err != nil {
			p.hub.log.Debug("添加待处理 ICE 候选失败", "error", err, "peer_id", p.ID)
		}
	}
}

func (p *RTCPeer) syncPublishedTracks() {
	if p.isClosed() {
		return
	}
	for _, pub := range p.room.publishedSnapshot() {
		p.attachIfWanted(pub)
	}
}

func (p *RTCPeer) requestOutboundKeyframes() {
	p.tracksMu.Lock()
	pubs := make([]*publishedTrack, 0, len(p.outbound))
	for _, out := range p.outbound {
		if out.pub.info.Kind == "video" {
			pubs = append(pubs, out.pub)
		}
	}
	p.tracksMu.Unlock()
	for _, pub := range pubs {
		pub.forceKeyframe()
	}
}

func (p *RTCPeer) isInitialSynced() bool {
	p.negMu.Lock()
	defer p.negMu.Unlock()
	return p.initialSynced
}

func (p *RTCPeer) wantsLocked(pub *publishedTrack) bool {
	if pub.owner == p {
		return false
	}
	if p.autoSubscribe {
		return true
	}
	_, ok := p.wanted[pub.key]
	return ok
}

func (p *RTCPeer) attachWantedLocked(pub *publishedTrack) {
	if !p.wantsLocked(pub) {
		return
	}
	p.attachPublished(pub)
}

func (p *RTCPeer) attachIfWanted(pub *publishedTrack) {
	if p.isClosed() {
		return
	}
	p.subMu.Lock()
	defer p.subMu.Unlock()
	if p.isClosed() || !p.isInitialSynced() {
		return
	}
	p.attachWantedLocked(pub)
}

// setSubscriptions replaces the participant's complete subscription state.
// The relay only executes the consumer's list; it never derives subscriptions
// from track kind, publisher, or any other business meaning.
func (p *RTCPeer) setSubscriptions(trackIDs []string, all bool) ([]string, bool) {
	normalized := []string{}
	if !all {
		normalized = normalizeTrackIDs(trackIDs)
	}

	p.subMu.Lock()
	defer p.subMu.Unlock()
	if p.isClosed() {
		return normalized, false
	}

	p.autoSubscribe = all
	p.wanted = nil
	if !all {
		p.wanted = make(map[string]struct{}, len(normalized))
		for _, id := range normalized {
			p.wanted[id] = struct{}{}
		}
	}

	if !p.isInitialSynced() {
		return normalized, true
	}

	if !all {
		p.tracksMu.Lock()
		stale := make([]string, 0, len(p.outbound))
		for key := range p.outbound {
			if _, keep := p.wanted[key]; !keep {
				stale = append(stale, key)
			}
		}
		p.tracksMu.Unlock()
		for _, key := range stale {
			p.removeOutbound(key)
		}
	}

	for _, pub := range p.room.publishedSnapshot() {
		p.attachWantedLocked(pub)
	}
	return normalized, true
}

func normalizeTrackIDs(trackIDs []string) []string {
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
	}
	return out
}

func (p *RTCPeer) handleTrack(remote *webrtc.TrackRemote, receiver *webrtc.RTPReceiver) {
	if p.isClosed() || p.room.isClosed() {
		return
	}
	key := newID("t_", 8)
	if !p.reservePublishedKey(key, p.hub.cfg.WebRTC.MaxTracksPerPeer) {
		p.hub.log.Warn("WebRTC 参与者轨道数量超限",
			"peer_id", p.ID, "room_id", p.room.ID, "limit", p.hub.cfg.WebRTC.MaxTracksPerPeer)
		p.sendSignal(protocol.RTCSignal{Type: protocol.TypeError, Error: "发布轨道数量超过上限"})
		return
	}
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
	pub := &publishedTrack{
		key:    key,
		room:   p.room,
		owner:  p,
		remote: remote,
		info:   info,
		subs:   make(map[*RTCPeer]*webrtc.TrackLocalStaticRTP),
	}
	if !p.room.publish(pub) {
		p.removePublishedKey(key)
		return
	}
	p.hub.notifyAgent(p.room.KeyID, protocol.TrackPublished{
		Type:        protocol.TypeTrackPublished,
		RoomID:      p.room.ID,
		RoomName:    p.room.Name,
		ConnID:      p.ID,
		Track:       info,
		PublishedAt: time.Now().UTC(),
	})
	go pub.readLoop()
	go func() {
		for {
			if _, _, err := receiver.ReadRTCP(); err != nil {
				return
			}
		}
	}()
	for _, peer := range p.room.peersSnapshot() {
		if peer == p {
			continue
		}
		peer.attachIfWanted(pub)
	}
	p.hub.log.Info("WebRTC 轨道已发布",
		"room_id", p.room.ID, "peer_id", p.ID, "track_id", key, "kind", info.Kind, "codec", info.Codec)
}

func (p *RTCPeer) attachPublished(pub *publishedTrack) {
	if p.isClosed() || pub.closed.Load() {
		return
	}
	p.opMu.Lock()
	if p.isClosed() {
		p.opMu.Unlock()
		return
	}
	p.tracksMu.Lock()
	_, exists := p.outbound[pub.key]
	p.tracksMu.Unlock()
	if exists {
		p.opMu.Unlock()
		return
	}
	codec := pub.remote.Codec().RTPCodecCapability
	local, err := webrtc.NewTrackLocalStaticRTP(codec, pub.info.ID, pub.info.StreamID)
	if err != nil {
		p.opMu.Unlock()
		p.hub.log.Warn("创建转发轨道失败", "error", err, "peer_id", p.ID, "track_id", pub.key)
		return
	}
	sender, err := p.pc.AddTrack(local)
	if err != nil {
		p.opMu.Unlock()
		p.hub.log.Warn("添加转发轨道失败", "error", err, "peer_id", p.ID, "track_id", pub.key)
		return
	}
	p.tracksMu.Lock()
	p.outbound[pub.key] = &outboundTrack{pub: pub, local: local, sender: sender}
	p.tracksMu.Unlock()
	if !pub.addSubscriber(p, local) {
		p.tracksMu.Lock()
		delete(p.outbound, pub.key)
		p.tracksMu.Unlock()
		_ = p.pc.RemoveTrack(sender)
		p.opMu.Unlock()
		return
	}
	p.tracksMu.Lock()
	p.everTracks = true
	p.tracksMu.Unlock()
	if pub.info.Kind == "video" {
		p.keyframeOnSync.Store(true)
	}
	info := pub.info
	p.sendSignal(protocol.RTCSignal{Type: protocol.TypeTrackAdded, Track: &info})
	p.opMu.Unlock()

	go p.drainRTCP(pub, sender)
	p.requestNegotiation()
}

func (p *RTCPeer) drainRTCP(pub *publishedTrack, sender *webrtc.RTPSender) {
	for {
		packets, _, err := sender.ReadRTCP()
		if err != nil {
			return
		}
		for _, packet := range packets {
			switch packet.(type) {
			case *rtcp.PictureLossIndication, *rtcp.FullIntraRequest:
				pub.requestKeyframe()
			}
		}
	}
}

func (p *RTCPeer) removeOutbound(key string) {
	p.tracksMu.Lock()
	out := p.outbound[key]
	delete(p.outbound, key)
	p.tracksMu.Unlock()
	if out == nil {
		return
	}
	out.pub.removeSubscriber(p)
	if p.isClosed() {
		return
	}
	p.opMu.Lock()
	if !p.isClosed() {
		_ = p.pc.RemoveTrack(out.sender)
	}
	p.opMu.Unlock()
	info := out.pub.info
	p.sendSignal(protocol.RTCSignal{Type: protocol.TypeTrackRemoved, Track: &info})
	p.requestNegotiation()
}

func (p *RTCPeer) detachAllTracks(reason string) {
	for _, pub := range p.room.publishedSnapshot() {
		if pub.owner == p {
			p.hub.removePublishedTrack(pub, reason)
			continue
		}
		pub.removeSubscriber(p)
		p.tracksMu.Lock()
		delete(p.outbound, pub.key)
		p.tracksMu.Unlock()
	}
	p.metaMu.Lock()
	p.pubKeys = make(map[string]struct{})
	p.metaMu.Unlock()
}

func (h *Hub) removePublishedTrack(t *publishedTrack, reason string) {
	if t == nil || !t.closed.CompareAndSwap(false, true) {
		return
	}
	t.room.unpublish(t.key)
	t.owner.hub.notifyAgent(t.room.KeyID, protocol.TrackUnpublished{
		Type:          protocol.TypeTrackUnpublished,
		RoomID:        t.room.ID,
		RoomName:      t.room.Name,
		ConnID:        t.owner.ID,
		Track:         t.info,
		Reason:        reason,
		UnpublishedAt: time.Now().UTC(),
	})
	t.subsMu.Lock()
	peers := make([]*RTCPeer, 0, len(t.subs))
	for peer := range t.subs {
		peers = append(peers, peer)
	}
	t.subs = make(map[*RTCPeer]*webrtc.TrackLocalStaticRTP)
	t.subsMu.Unlock()
	for _, peer := range peers {
		peer.removeOutbound(t.key)
	}
	t.owner.removePublishedKey(t.key)
	h.log.Info("WebRTC 轨道已移除",
		"room_id", t.room.ID, "peer_id", t.owner.ID, "track_id", t.key, "reason", reason)
}

func (p *RTCPeer) requestNegotiation() {
	p.negMu.Lock()
	if p.isClosed() {
		p.negMu.Unlock()
		return
	}
	p.needOffer = true
	if !p.initialSynced || p.negotiating || p.pendingOffer {
		p.negMu.Unlock()
		return
	}
	p.negotiating = true
	p.negMu.Unlock()
	go p.negotiate()
}

func (p *RTCPeer) negotiate() {
	p.negMu.Lock()
	if p.isClosed() || !p.initialSynced || !p.needOffer {
		p.negotiating = false
		p.negMu.Unlock()
		return
	}
	p.needOffer = false
	p.negMu.Unlock()

	p.opMu.Lock()
	if p.isClosed() {
		p.opMu.Unlock()
		p.negMu.Lock()
		p.negotiating = false
		p.negMu.Unlock()
		return
	}
	if p.pc.SignalingState() != webrtc.SignalingStateStable {
		p.opMu.Unlock()
		p.negMu.Lock()
		p.needOffer = true
		p.negotiating = false
		p.negMu.Unlock()
		return
	}
	p.tracksMu.Lock()
	trackCount := len(p.outbound)
	ever := p.everTracks
	p.tracksMu.Unlock()
	if trackCount == 0 && !ever {
		p.opMu.Unlock()
		p.negMu.Lock()
		p.negotiating = false
		p.negMu.Unlock()
		return
	}
	offer, err := p.pc.CreateOffer(nil)
	if err != nil {
		p.opMu.Unlock()
		p.hub.log.Warn("创建 WebRTC offer 失败", "error", err, "peer_id", p.ID)
		p.negMu.Lock()
		p.negotiating = false
		p.negMu.Unlock()
		go p.finish("sdp_error")
		return
	}
	if err := p.pc.SetLocalDescription(offer); err != nil {
		p.opMu.Unlock()
		p.hub.log.Warn("设置 WebRTC offer 失败", "error", err, "peer_id", p.ID)
		p.negMu.Lock()
		p.negotiating = false
		p.negMu.Unlock()
		go p.finish("sdp_error")
		return
	}
	local := p.pc.LocalDescription()
	p.opMu.Unlock()
	if local == nil {
		p.negMu.Lock()
		p.negotiating = false
		p.negMu.Unlock()
		go p.finish("sdp_error")
		return
	}

	p.negMu.Lock()
	p.pendingOffer = true
	p.negotiating = false
	p.stopOfferTimerLocked()
	var timer *time.Timer
	timer = time.AfterFunc(peerOfferTimeout, func() {
		p.negMu.Lock()
		timedOut := p.negotiateTimer == timer && p.pendingOffer && !p.isClosed()
		p.negMu.Unlock()
		if timedOut {
			p.hub.log.Warn("WebRTC 重协商超时", "peer_id", p.ID)
			go p.finish("negotiation_timeout")
		}
	})
	p.negotiateTimer = timer
	p.negMu.Unlock()
	p.sendSignal(protocol.RTCSignal{Type: protocol.TypeOffer, SDP: local.SDP})
}

// stopOfferTimerLocked must be called with negMu held.
func (p *RTCPeer) stopOfferTimerLocked() {
	if p.negotiateTimer != nil {
		p.negotiateTimer.Stop()
		p.negotiateTimer = nil
	}
}

func (p *RTCPeer) finish(reason string) {
	p.finishOnce.Do(func() {
		p.reasonMu.Lock()
		p.reason = reason
		p.reasonMu.Unlock()
		p.closed.Store(true)
		p.hub.removePeer(p, reason)
		p.closeWith(websocket.CloseNormalClosure, "closed")
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
		p.negMu.Lock()
		p.stopOfferTimerLocked()
		p.negMu.Unlock()
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

func (p *RTCPeer) isClosed() bool { return p.closed.Load() }

func (p *RTCPeer) reservePublishedKey(key string, limit int) bool {
	p.metaMu.Lock()
	defer p.metaMu.Unlock()
	if len(p.pubKeys) >= limit {
		return false
	}
	p.pubKeys[key] = struct{}{}
	return true
}

func (p *RTCPeer) removePublishedKey(key string) {
	p.metaMu.Lock()
	delete(p.pubKeys, key)
	p.metaMu.Unlock()
}

func (p *RTCPeer) publishedCount() int {
	p.metaMu.Lock()
	defer p.metaMu.Unlock()
	return len(p.pubKeys)
}

func (t *publishedTrack) addSubscriber(peer *RTCPeer, local *webrtc.TrackLocalStaticRTP) bool {
	t.subsMu.Lock()
	defer t.subsMu.Unlock()
	if t.closed.Load() {
		return false
	}
	t.subs[peer] = local
	return true
}

func (t *publishedTrack) removeSubscriber(peer *RTCPeer) {
	t.subsMu.Lock()
	delete(t.subs, peer)
	t.subsMu.Unlock()
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

func (t *publishedTrack) forceKeyframe() {
	t.lastPLI.Store(time.Now().UnixMilli())
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
		t.owner.hub.log.Debug("发送关键帧请求失败", "track_id", t.key, "error", err)
	}
}

func (t *publishedTrack) readLoop() {
	buffer := make([]byte, 8192)
	var packet rtp.Packet
	var lastVideoPacket time.Time
	for {
		n, _, err := t.remote.Read(buffer)
		if err != nil {
			t.owner.hub.removePublishedTrack(t, "track_ended")
			return
		}
		if err := packet.Unmarshal(buffer[:n]); err != nil {
			continue
		}
		now := time.Now()
		resumed := t.info.Kind == "video" &&
			!lastVideoPacket.IsZero() &&
			now.Sub(lastVideoPacket) >= keyframeGap
		if t.info.Kind == "video" {
			lastVideoPacket = now
		}
		t.subsMu.Lock()
		targets := make([]*webrtc.TrackLocalStaticRTP, 0, len(t.subs))
		for _, local := range t.subs {
			targets = append(targets, local)
		}
		t.subsMu.Unlock()
		if len(targets) == 0 {
			continue
		}
		if resumed {
			t.forceKeyframe()
		}
		for _, local := range targets {
			_ = local.WriteRTP(&packet)
		}
		t.room.recordRTP(int64(n))
		t.owner.hub.rtpPackets.Add(1)
		t.owner.hub.rtpBytes.Add(int64(n))
	}
}
