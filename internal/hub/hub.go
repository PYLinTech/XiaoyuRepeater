package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pion/webrtc/v4"

	"xiaoyurepeater/internal/protocol"
)

type Config struct {
	MaxRoomsPerKey         int
	DefaultMaxParticipants int
	MaxParticipantsCap     int
	MaxFrameBytes          int64
	AuthTimeout            time.Duration
	AgentSubscriptionQueue int
	RoomSweepInterval      time.Duration
	RoomOrphanTTL          time.Duration
	WebRTC                 WebRTCConfig
}

type WebRTCConfig struct {
	Enabled           bool
	UDPPortMin        int
	UDPPortMax        int
	UDPMuxPort        int
	TCPMuxPort        int
	PublicIPs         []string
	ICEServers        []string
	MaxTracksPerPeer  int
	VideoQueuePackets int
	AudioQueuePackets int
	ConnectTimeout    time.Duration
	PendingICELimit   int
	MaxDesiredTracks  int
	DesiredTTL        time.Duration
}

type Hub struct {
	cfg        Config
	log        *slog.Logger
	start      time.Time
	rtcAPI     *webrtc.API
	iceServers []webrtc.ICEServer
	rtcClosers []io.Closer

	mu     sync.RWMutex
	rooms  map[string]*Room
	agents map[string]*Agent
	closed bool

	stop     chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup

	events *eventLog

	messagesRelayed     atomic.Int64
	bytesRelayed        atomic.Int64
	rtpPackets          atomic.Int64
	rtpBytes            atomic.Int64
	rtpQueueDepth       atomic.Int64
	rtpQueueDropped     atomic.Int64
	keyframeRequests    atomic.Int64
	negotiationFailures atomic.Int64
	iceRestarts         atomic.Int64
	roomsReclaimed      atomic.Int64
	joinsTotal          atomic.Int64
	roomsCreated        atomic.Int64
	authRequests        atomic.Int64
	authDenied          atomic.Int64
}

func New(cfg Config, log *slog.Logger) (*Hub, error) {
	if cfg.RoomSweepInterval <= 0 {
		cfg.RoomSweepInterval = 15 * time.Second
	}
	if cfg.RoomOrphanTTL <= 0 {
		cfg.RoomOrphanTTL = 30 * time.Minute
	}
	if cfg.AgentSubscriptionQueue <= 0 {
		cfg.AgentSubscriptionQueue = 256
	}
	if cfg.WebRTC.VideoQueuePackets <= 0 {
		cfg.WebRTC.VideoQueuePackets = 256
	}
	if cfg.WebRTC.AudioQueuePackets <= 0 {
		cfg.WebRTC.AudioQueuePackets = 200
	}
	if cfg.WebRTC.MaxDesiredTracks <= 0 {
		cfg.WebRTC.MaxDesiredTracks = 512
	}
	if cfg.WebRTC.DesiredTTL <= 0 {
		cfg.WebRTC.DesiredTTL = 5 * time.Minute
	}
	h := &Hub{
		cfg:    cfg,
		log:    log,
		start:  time.Now().UTC(),
		rooms:  make(map[string]*Room),
		agents: make(map[string]*Agent),
		events: newEventLog(100),
		stop:   make(chan struct{}),
	}
	if cfg.WebRTC.Enabled {
		api, closers, err := newWebRTCAPI(cfg.WebRTC)
		if err != nil {
			return nil, err
		}
		h.rtcAPI = api
		h.rtcClosers = closers
		if len(cfg.WebRTC.ICEServers) > 0 {
			h.iceServers = []webrtc.ICEServer{{URLs: append([]string(nil), cfg.WebRTC.ICEServers...)}}
		}
	}
	h.wg.Add(1)
	go h.janitor()
	return h, nil
}

func (h *Hub) StartedAt() time.Time { return h.start }

// RoomOptions is the generic creation contract. Metadata is opaque JSON that
// the relay stores and returns unchanged; it never interprets its structure.
type RoomOptions struct {
	Name            string
	Metadata        json.RawMessage
	MaxParticipants int
}

func normalizeMetadata(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 || strings.TrimSpace(string(raw)) == "null" {
		return nil, nil
	}
	if !json.Valid(raw) {
		return nil, fmt.Errorf("metadata 必须是有效 JSON")
	}
	return cloneMetadata(raw), nil
}

func cloneMetadata(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	return append(json.RawMessage(nil), raw...)
}

func (h *Hub) CreateRoom(keyID, keyName string, options RoomOptions) (RoomInfo, *Error) {
	name := cleanString(strings.TrimSpace(options.Name), 80, "未命名房间")
	metadata, err := normalizeMetadata(options.Metadata)
	if err != nil {
		return RoomInfo{}, hError(400, "invalid_metadata", err.Error())
	}
	maxParticipants := options.MaxParticipants
	if maxParticipants == 0 {
		maxParticipants = h.cfg.DefaultMaxParticipants
	}
	if maxParticipants < 2 || maxParticipants > h.cfg.MaxParticipantsCap {
		return RoomInfo{}, hError(400, "invalid_max_participants",
			fmt.Sprintf("房间人数上限需要在 2 到 %d 之间", h.cfg.MaxParticipantsCap))
	}

	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return RoomInfo{}, ErrHubClosed
	}
	count := 0
	for _, room := range h.rooms {
		if room.KeyID == keyID {
			count++
		}
	}
	if count >= h.cfg.MaxRoomsPerKey {
		h.mu.Unlock()
		return RoomInfo{}, ErrTooManyRooms
	}
	id := newID("r_", 9)
	for h.rooms[id] != nil {
		id = newID("r_", 9)
	}
	room := &Room{
		ID:              id,
		Name:            name,
		Metadata:        metadata,
		KeyID:           keyID,
		KeyName:         keyName,
		MaxParticipants: maxParticipants,
		CreatedAt:       time.Now().UTC(),
		participants:    make(map[string]*Participant),
		peers:           make(map[string]*RTCPeer),
		published:       make(map[string]*publishedTrack),
	}
	room.touch()
	if h.agents[keyID] == nil {
		room.markAgentOffline()
	}
	h.rooms[id] = room
	online := h.agents[keyID] != nil
	h.roomsCreated.Add(1)
	h.events.add("room_created", eventRoomCreated(name), id, "")
	h.mu.Unlock()

	return room.snapshot(online), nil
}

func (h *Hub) GetRoom(id string) (RoomInfo, bool) {
	h.mu.RLock()
	room := h.rooms[id]
	if room == nil {
		h.mu.RUnlock()
		return RoomInfo{}, false
	}
	online := h.agents[room.KeyID] != nil
	h.mu.RUnlock()
	return room.snapshot(online), true
}

func (h *Hub) ListRooms() []RoomInfo {
	h.mu.RLock()
	rooms := make([]*Room, 0, len(h.rooms))
	for _, room := range h.rooms {
		rooms = append(rooms, room)
	}
	online := make(map[string]bool, len(h.agents))
	for keyID := range h.agents {
		online[keyID] = true
	}
	h.mu.RUnlock()

	out := make([]RoomInfo, 0, len(rooms))
	for _, room := range rooms {
		out = append(out, room.snapshot(online[room.KeyID]))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

func (h *Hub) RoomsForKey(keyID string) []RoomInfo {
	all := h.ListRooms()
	out := make([]RoomInfo, 0, len(all))
	for _, room := range all {
		if room.KeyID == keyID {
			out = append(out, room)
		}
	}
	return out
}

func (h *Hub) KeyStatus(keyID string) (online bool, rooms, participants int) {
	all := h.ListRooms()
	for _, room := range all {
		if room.KeyID == keyID {
			rooms++
			participants += room.ParticipantCount
		}
	}
	h.mu.RLock()
	_, online = h.agents[keyID]
	h.mu.RUnlock()
	return online, rooms, participants
}

func (h *Hub) CloseRoom(id, reason string) bool {
	h.mu.Lock()
	room := h.rooms[id]
	if room == nil {
		h.mu.Unlock()
		return false
	}
	delete(h.rooms, id)
	h.mu.Unlock()
	h.closeRoom(room, reason, true)
	return true
}

func (h *Hub) CloseRoomsForKey(keyID, reason string) int {
	h.mu.Lock()
	var rooms []*Room
	for id, room := range h.rooms {
		if room.KeyID == keyID {
			rooms = append(rooms, room)
			delete(h.rooms, id)
		}
	}
	agent := h.agents[keyID]
	delete(h.agents, keyID)
	h.mu.Unlock()

	for _, room := range rooms {
		h.closeRoom(room, reason, agent != nil)
	}
	if agent != nil {
		agent.closeWith(4003, reason)
		agent.failPending(ErrAgentOffline)
	}
	return len(rooms)
}

func (h *Hub) DisconnectAgent(keyID, reason string) {
	h.mu.Lock()
	agent := h.agents[keyID]
	delete(h.agents, keyID)
	for _, room := range h.rooms {
		if room.KeyID == keyID {
			room.markAgentOffline()
		}
	}
	h.mu.Unlock()
	if agent != nil {
		agent.closeWith(4003, reason)
		agent.failPending(ErrAgentOffline)
	}
}

// janitor reclaims only objects that are provably expired: rooms whose owning
// agent has been offline past the lease TTL, or rooms already marked closed
// but still mapped. Live rooms are never touched.
func (h *Hub) janitor() {
	defer h.wg.Done()
	ticker := time.NewTicker(h.cfg.RoomSweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-h.stop:
			return
		case now := <-ticker.C:
			h.sweep(now)
		}
	}
}

func (h *Hub) sweep(now time.Time) {
	var expired []*Room
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}
	for id, room := range h.rooms {
		if room.isClosed() {
			delete(h.rooms, id)
			continue
		}
		offlineAt := room.agentOfflineAt()
		if offlineAt.IsZero() {
			continue
		}
		if now.Sub(offlineAt) < h.cfg.RoomOrphanTTL {
			continue
		}
		delete(h.rooms, id)
		expired = append(expired, room)
	}
	h.mu.Unlock()

	for _, room := range expired {
		h.roomsReclaimed.Add(1)
		h.closeRoom(room, "room_expired", false)
		h.log.Info("回收失联使用方的房间",
			"room_id", room.ID, "key_id", room.KeyID,
			"ttl", h.cfg.RoomOrphanTTL.String(),
			"tracks", room.publishedCount())
	}
}

func (h *Hub) closeRoom(room *Room, reason string, notifyAgent bool) {
	participants, peers := room.closeAndDetach()
	for _, p := range participants {
		p.closeWith(4000, reason)
	}
	for _, p := range peers {
		p.closeWith(4000, reason)
	}
	if notifyAgent {
		h.notifyAgent(room.KeyID, protocol.RoomClosed{
			Type:     protocol.TypeRoomClosed,
			RoomID:   room.ID,
			RoomName: room.Name,
			Reason:   reason,
			ClosedAt: time.Now().UTC(),
		})
	}
	h.events.add("room_closed", eventRoomClosed(room.Name, reason), room.ID, "")
}

func (h *Hub) AuthorizeJoin(ctx context.Context, roomID, transport, remoteAddr string, query, headers map[string][]string) (*Room, Decision, *Error) {
	h.mu.RLock()
	room := h.rooms[roomID]
	var agent *Agent
	if room != nil {
		agent = h.agents[room.KeyID]
	}
	h.mu.RUnlock()

	if room == nil {
		return nil, Decision{}, ErrRoomNotFound
	}
	if room.isClosed() {
		return nil, Decision{}, ErrRoomClosed
	}
	if room.isFull() {
		return nil, Decision{}, ErrRoomFull
	}
	if agent == nil {
		return nil, Decision{}, ErrAgentOffline
	}

	requestID := newID("aq_", 9)
	ch, ok := agent.addPending(requestID)
	if !ok {
		return nil, Decision{}, ErrAgentBusy
	}
	request := protocol.AuthRequest{
		Type:        protocol.TypeAuthRequest,
		RequestID:   requestID,
		RoomID:      room.ID,
		RoomName:    room.Name,
		Metadata:    cloneMetadata(room.Metadata),
		Transport:   transport,
		RemoteAddr:  remoteAddr,
		Query:       query,
		Headers:     headers,
		RequestedAt: time.Now().UTC(),
	}
	if !agent.sendJSON(request) {
		agent.removePending(requestID, ch)
		return nil, Decision{}, ErrAgentOffline
	}
	h.authRequests.Add(1)

	timer := time.NewTimer(h.cfg.AuthTimeout)
	defer timer.Stop()

	select {
	case outcome := <-ch:
		if outcome.Err != nil {
			h.authDenied.Add(1)
			return nil, Decision{}, outcome.Err
		}
		return room, outcome.Decision, nil
	case <-timer.C:
		agent.removePending(requestID, ch)
		h.authDenied.Add(1)
		h.log.Warn("使用方鉴权超时", "room_id", room.ID, "key_id", room.KeyID, "request_id", requestID)
		return nil, Decision{}, ErrAuthTimeout
	case <-ctx.Done():
		agent.removePending(requestID, ch)
		return nil, Decision{}, hError(499, "client_canceled", "客户端已断开")
	case <-agent.done:
		agent.removePending(requestID, ch)
		return nil, Decision{}, ErrAgentOffline
	}
}

// ServeParticipant upgrades the HTTP request and relays frames until the
// participant leaves. The returned error is only for failures before the
// WebSocket handshake; after that all failures are reported via close frames.
func (h *Hub) ServeParticipant(w http.ResponseWriter, r *http.Request, room *Room, decision Decision, remoteAddr string) *Error {
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

	upgrader := websocket.Upgrader{
		HandshakeTimeout: 10 * time.Second,
		ReadBufferSize:   4096,
		WriteBufferSize:  4096,
		CheckOrigin:      func(*http.Request) bool { return true },
	}
	ws, err := upgrader.Upgrade(w, r, headers)
	if err != nil {
		h.log.Debug("用户 WebSocket 升级失败", "error", err, "room_id", room.ID)
		return nil
	}

	p := newParticipant(h, room, ws, decision, remoteAddr, r.UserAgent())
	if joinErr := room.addParticipant(p); joinErr != nil {
		p.closeWith(4004, joinErr.Code)
		return nil
	}

	h.joinsTotal.Add(1)
	h.events.add("user_joined",
		fmt.Sprintf("%s 加入了房间「%s」", p.Subject, room.Name), room.ID, p.Subject)
	h.notifyAgent(room.KeyID, protocol.UserJoined{
		Type:       protocol.TypeUserJoined,
		RoomID:     room.ID,
		RoomName:   room.Name,
		ConnID:     p.ID,
		Transport:  protocol.TransportWebSocket,
		Subject:    p.Subject,
		RemoteAddr: p.RemoteAddr,
		JoinedAt:   p.JoinedAt,
	})
	h.log.Info("用户加入房间",
		"room_id", room.ID, "conn_id", p.ID, "subject", p.Subject, "remote_addr", p.RemoteAddr)

	p.run()

	h.log.Info("用户离开房间",
		"room_id", room.ID, "conn_id", p.ID, "subject", p.Subject, "reason", p.finishReason())
	return nil
}

func (h *Hub) removeParticipant(p *Participant, reason string) {
	if !p.room.removeParticipant(p.ID) {
		return
	}
	h.events.add("user_left",
		fmt.Sprintf("%s 离开了房间「%s」", p.Subject, p.room.Name), p.room.ID, p.Subject)
	h.notifyAgent(p.room.KeyID, protocol.UserLeft{
		Type:      protocol.TypeUserLeft,
		RoomID:    p.room.ID,
		RoomName:  p.room.Name,
		ConnID:    p.ID,
		Transport: protocol.TransportWebSocket,
		Subject:   p.Subject,
		Reason:    reason,
		LeftAt:    time.Now().UTC(),
	})
}

func (h *Hub) relay(sender *Participant, messageType int, data []byte) {
	room := sender.room
	room.touch()
	room.mu.RLock()
	if room.closed {
		room.mu.RUnlock()
		return
	}
	targets := make([]*Participant, 0, len(room.participants))
	for _, p := range room.participants {
		if p != sender {
			targets = append(targets, p)
		}
	}
	room.mu.RUnlock()
	if len(targets) == 0 {
		return
	}

	payload := append([]byte(nil), data...)
	var slow []*Participant
	delivered := 0
	for _, target := range targets {
		if target.trySend(messageType, payload) {
			delivered++
		} else {
			slow = append(slow, target)
		}
	}
	for _, target := range slow {
		target.closeWith(websocket.ClosePolicyViolation, "slow consumer")
		target.finish("slow_consumer")
	}
	if delivered > 0 {
		room.recordTraffic(int64(len(payload)))
		h.messagesRelayed.Add(1)
		h.bytesRelayed.Add(int64(len(payload)))
	}
}

func (h *Hub) notifyAgent(keyID string, payload any) {
	h.mu.RLock()
	agent := h.agents[keyID]
	h.mu.RUnlock()
	if agent != nil {
		agent.sendJSON(payload)
	}
}

func (h *Hub) ServeAgent(ws *websocket.Conn, keyID, keyName, remoteAddr string) {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		_ = ws.WriteControl(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseGoingAway, "server shutting down"),
			time.Now().Add(agentCloseTimeout))
		_ = ws.Close()
		return
	}
	old := h.agents[keyID]
	agent := newAgent(h, ws, keyID, keyName, remoteAddr)
	h.agents[keyID] = agent
	refs := make([]protocol.RoomRef, 0)
	var peers []*RTCPeer
	for _, room := range h.rooms {
		if room.KeyID != keyID {
			continue
		}
		room.markAgentOnline()
		room.touch()
		connections := room.connectionRefs()
		refs = append(refs, protocol.RoomRef{
			ID:           room.ID,
			Name:         room.Name,
			Metadata:     cloneMetadata(room.Metadata),
			Participants: len(connections),
			CreatedAt:    room.CreatedAt,
			Connections:  connections,
			Tracks:       room.trackInfos(),
		})
		peers = append(peers, room.peersSnapshot()...)
	}
	// Enqueue hello before releasing the hub lock so no room event can be
	// delivered ahead of the handshake snapshot.
	agent.sendJSON(protocol.Hello{
		Type:       protocol.TypeHello,
		Protocol:   protocol.Version,
		AgentID:    agent.id,
		KeyID:      keyID,
		KeyName:    keyName,
		ServerTime: time.Now().UTC(),
		Rooms:      refs,
		Features:   []string{"subscription_ack", "subscription_snapshot", "room_metadata", "disconnect_connection"},
		Limits: protocol.Limits{
			MaxRooms:             h.cfg.MaxRoomsPerKey,
			MaxFrameBytes:        h.cfg.MaxFrameBytes,
			AuthTimeoutMillis:    int(h.cfg.AuthTimeout / time.Millisecond),
			MaxParticipantsLimit: h.cfg.MaxParticipantsCap,
			WebRTC:               h.rtcAPI != nil,
			MaxTracksPerPeer:     h.cfg.WebRTC.MaxTracksPerPeer,
		},
	})
	h.mu.Unlock()

	if old != nil {
		h.log.Info("使用方重复连接，替换旧连接", "key_id", keyID)
		old.closeWith(4001, "replaced by new connection")
		old.failPending(ErrAgentOffline)
	}

	// Re-apply stored desired subscriptions so the snapshot and the peers
	// agree even if tracks were published while the agent was offline.
	for _, peer := range peers {
		peer.reconcileAsync()
	}

	go agent.writeLoop()
	go agent.subscriptionLoop()
	h.events.add("agent_online", fmt.Sprintf("使用方「%s」已连接", keyName), "", "")
	h.log.Info("使用方已连接", "agent_id", agent.id, "key_id", keyID, "name", keyName, "remote_addr", remoteAddr)

	agent.readLoop()

	h.mu.Lock()
	wasCurrent := h.agents[keyID] == agent
	if wasCurrent {
		delete(h.agents, keyID)
		for _, room := range h.rooms {
			if room.KeyID == keyID {
				room.markAgentOffline()
			}
		}
	}
	h.mu.Unlock()
	agent.closeWith(websocket.CloseNormalClosure, "agent disconnected")
	agent.failPending(ErrAgentOffline)
	if wasCurrent {
		h.events.add("agent_offline", fmt.Sprintf("使用方「%s」已断开", keyName), "", "")
		h.log.Info("使用方已断开", "agent_id", agent.id, "key_id", keyID, "name", keyName)
	}
}

func (h *Hub) Snapshot() Snapshot {
	h.mu.RLock()
	rooms := make([]*Room, 0, len(h.rooms))
	for _, room := range h.rooms {
		rooms = append(rooms, room)
	}
	agents := make([]*Agent, 0, len(h.agents))
	for _, agent := range h.agents {
		agents = append(agents, agent)
	}
	online := make(map[string]bool, len(h.agents))
	for keyID := range h.agents {
		online[keyID] = true
	}
	closed := h.closed
	h.mu.RUnlock()

	roomInfos := make([]RoomInfo, 0, len(rooms))
	roomCount := make(map[string]int)
	participantCount := make(map[string]int)
	totalParticipants := 0
	totalRTCPeers := 0
	totalRTCTracks := 0
	totalSubscriptions := 0
	for _, room := range rooms {
		info := room.snapshot(online[room.KeyID])
		roomInfos = append(roomInfos, info)
		roomCount[room.KeyID]++
		participantCount[room.KeyID] += info.ParticipantCount
		totalParticipants += info.ParticipantCount
		totalRTCPeers += info.RTCPeers
		totalRTCTracks += info.Tracks
		totalSubscriptions += info.RTCSubscriptions
	}
	sort.Slice(roomInfos, func(i, j int) bool { return roomInfos[i].CreatedAt.After(roomInfos[j].CreatedAt) })

	agentInfos := make([]AgentInfo, 0, len(agents))
	for _, agent := range agents {
		agentInfos = append(agentInfos, AgentInfo{
			AgentID:      agent.id,
			KeyID:        agent.keyID,
			KeyName:      agent.keyName,
			RemoteAddr:   agent.remoteAddr,
			ConnectedAt:  agent.connected,
			Rooms:        roomCount[agent.keyID],
			Participants: participantCount[agent.keyID],
		})
	}
	sort.Slice(agentInfos, func(i, j int) bool { return agentInfos[i].ConnectedAt.After(agentInfos[j].ConnectedAt) })

	return Snapshot{
		StartedAt:     h.start,
		UptimeSeconds: int64(time.Since(h.start).Seconds()),
		Stats: StatsSnapshot{
			AgentsOnline:        len(agents),
			ActiveRooms:         len(rooms),
			Participants:        totalParticipants,
			RTCPeers:            totalRTCPeers,
			RTCTracks:           totalRTCTracks,
			RTCSubscriptions:    totalSubscriptions,
			Closed:              closed,
			MessagesRelayed:     h.messagesRelayed.Load(),
			BytesRelayed:        h.bytesRelayed.Load(),
			RTPPackets:          h.rtpPackets.Load(),
			RTPBytes:            h.rtpBytes.Load(),
			RTPQueueDepth:       h.rtpQueueDepth.Load(),
			RTPQueueDropped:     h.rtpQueueDropped.Load(),
			KeyframeRequests:    h.keyframeRequests.Load(),
			NegotiationFailures: h.negotiationFailures.Load(),
			ICERestarts:         h.iceRestarts.Load(),
			RoomsReclaimed:      h.roomsReclaimed.Load(),
			JoinsTotal:          h.joinsTotal.Load(),
			RoomsCreated:        h.roomsCreated.Load(),
			AuthRequests:        h.authRequests.Load(),
			AuthDenied:          h.authDenied.Load(),
		},
		Rooms:  roomInfos,
		Agents: agentInfos,
		Events: h.events.list(),
	}
}

func (h *Hub) Shutdown() {
	h.stopOnce.Do(func() { close(h.stop) })
	h.wg.Wait()

	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}
	h.closed = true
	agents := make([]*Agent, 0, len(h.agents))
	for keyID, agent := range h.agents {
		agents = append(agents, agent)
		delete(h.agents, keyID)
	}
	rooms := make([]*Room, 0, len(h.rooms))
	for id, room := range h.rooms {
		rooms = append(rooms, room)
		delete(h.rooms, id)
	}
	h.mu.Unlock()

	for _, agent := range agents {
		agent.closeWith(websocket.CloseGoingAway, "server shutting down")
		agent.failPending(ErrHubClosed)
	}
	for _, room := range rooms {
		participants, peers := room.closeAndDetach()
		for _, p := range participants {
			p.closeWith(websocket.CloseGoingAway, "server shutting down")
		}
		for _, p := range peers {
			p.closeWith(websocket.CloseGoingAway, "server shutting down")
		}
		h.events.add("room_closed", eventRoomClosed(room.Name, "server_shutdown"), room.ID, "")
	}
	for _, closer := range h.rtcClosers {
		_ = closer.Close()
	}
}

type StatsSnapshot struct {
	AgentsOnline        int   `json:"agents_online"`
	ActiveRooms         int   `json:"active_rooms"`
	Participants        int   `json:"participants"`
	RTCPeers            int   `json:"rtc_peers"`
	RTCTracks           int   `json:"rtc_tracks"`
	RTCSubscriptions    int   `json:"rtc_subscriptions"`
	Closed              bool  `json:"closed"`
	MessagesRelayed     int64 `json:"messages_relayed"`
	BytesRelayed        int64 `json:"bytes_relayed"`
	RTPPackets          int64 `json:"rtp_packets"`
	RTPBytes            int64 `json:"rtp_bytes"`
	RTPQueueDepth       int64 `json:"rtp_queue_depth"`
	RTPQueueDropped     int64 `json:"rtp_queue_dropped"`
	KeyframeRequests    int64 `json:"keyframe_requests"`
	NegotiationFailures int64 `json:"negotiation_failures"`
	ICERestarts         int64 `json:"ice_restarts"`
	RoomsReclaimed      int64 `json:"rooms_reclaimed"`
	JoinsTotal          int64 `json:"joins_total"`
	RoomsCreated        int64 `json:"rooms_created"`
	AuthRequests        int64 `json:"auth_requests"`
	AuthDenied          int64 `json:"auth_denied"`
}

type AgentInfo struct {
	AgentID      string    `json:"agent_id"`
	KeyID        string    `json:"key_id"`
	KeyName      string    `json:"key_name"`
	RemoteAddr   string    `json:"remote_addr"`
	ConnectedAt  time.Time `json:"connected_at"`
	Rooms        int       `json:"rooms"`
	Participants int       `json:"participants"`
}

type Snapshot struct {
	StartedAt     time.Time     `json:"started_at"`
	UptimeSeconds int64         `json:"uptime_seconds"`
	Stats         StatsSnapshot `json:"stats"`
	Rooms         []RoomInfo    `json:"rooms"`
	Agents        []AgentInfo   `json:"agents"`
	Events        []Event       `json:"events"`
}
