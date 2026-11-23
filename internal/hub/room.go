package hub

import (
	"encoding/json"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"xiaoyurepeater/internal/protocol"
)

type Room struct {
	ID              string
	Name            string
	Metadata        json.RawMessage
	KeyID           string
	KeyName         string
	MaxParticipants int
	CreatedAt       time.Time

	mu           sync.RWMutex
	participants map[string]*Participant
	peers        map[string]*RTCPeer
	closed       bool

	rtcMu     sync.Mutex
	published map[string]*publishedTrack

	activity     atomic.Int64
	agentOffline atomic.Int64
	messages     atomic.Int64
	bytes        atomic.Int64
	rtpPackets   atomic.Int64
	rtpBytes     atomic.Int64
	rtpDropped   atomic.Int64
}

func (r *Room) touch() {
	r.activity.Store(time.Now().UnixNano())
}

func (r *Room) lastActivity() time.Time {
	if ns := r.activity.Load(); ns > 0 {
		return time.Unix(0, ns)
	}
	return r.CreatedAt
}

func (r *Room) markAgentOnline() {
	r.agentOffline.Store(0)
}

func (r *Room) markAgentOffline() {
	r.agentOffline.CompareAndSwap(0, time.Now().UnixNano())
}

func (r *Room) agentOfflineAt() time.Time {
	if ns := r.agentOffline.Load(); ns > 0 {
		return time.Unix(0, ns)
	}
	return time.Time{}
}

func (r *Room) addParticipant(p *Participant) *Error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return ErrRoomClosed
	}
	if len(r.participants)+len(r.peers) >= r.MaxParticipants {
		return ErrRoomFull
	}
	r.participants[p.ID] = p
	r.touch()
	return nil
}

func (r *Room) addPeer(p *RTCPeer) *Error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return ErrRoomClosed
	}
	if len(r.participants)+len(r.peers) >= r.MaxParticipants {
		return ErrRoomFull
	}
	r.peers[p.ID] = p
	r.touch()
	return nil
}

func (r *Room) removeParticipant(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.participants[id]; !ok {
		return false
	}
	delete(r.participants, id)
	r.touch()
	return true
}

func (r *Room) removePeer(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.peers[id]; !ok {
		return false
	}
	delete(r.peers, id)
	r.touch()
	return true
}

func (r *Room) peersSnapshot() []*RTCPeer {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*RTCPeer, 0, len(r.peers))
	for _, p := range r.peers {
		out = append(out, p)
	}
	return out
}

func (r *Room) peerByID(id string) *RTCPeer {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.peers[id]
}

func (r *Room) isFull() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.participants)+len(r.peers) >= r.MaxParticipants
}

func (r *Room) isClosed() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.closed
}

// closeAndDetach marks the room closed, empties the member maps and returns
// the connections that still need to be closed.
func (r *Room) closeAndDetach() ([]*Participant, []*RTCPeer) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil, nil
	}
	r.closed = true
	participants := make([]*Participant, 0, len(r.participants))
	for _, p := range r.participants {
		participants = append(participants, p)
	}
	peers := make([]*RTCPeer, 0, len(r.peers))
	for _, p := range r.peers {
		peers = append(peers, p)
	}
	r.participants = make(map[string]*Participant)
	r.peers = make(map[string]*RTCPeer)
	return participants, peers
}

func (r *Room) publish(track *publishedTrack) bool {
	if r.isClosed() {
		return false
	}
	r.rtcMu.Lock()
	defer r.rtcMu.Unlock()
	if r.published == nil {
		r.published = make(map[string]*publishedTrack)
	}
	r.published[track.key] = track
	r.touch()
	return true
}

func (r *Room) unpublish(key string) *publishedTrack {
	r.rtcMu.Lock()
	defer r.rtcMu.Unlock()
	track := r.published[key]
	if track != nil {
		delete(r.published, key)
	}
	return track
}

func (r *Room) publishedSnapshot() []*publishedTrack {
	r.rtcMu.Lock()
	defer r.rtcMu.Unlock()
	out := make([]*publishedTrack, 0, len(r.published))
	for _, track := range r.published {
		out = append(out, track)
	}
	return out
}

func (r *Room) publishedCount() int {
	r.rtcMu.Lock()
	defer r.rtcMu.Unlock()
	return len(r.published)
}

func (r *Room) trackInfos() []protocol.RTCTrack {
	tracks := r.publishedSnapshot()
	out := make([]protocol.RTCTrack, 0, len(tracks))
	for _, track := range tracks {
		out = append(out, track.info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (r *Room) connectionRefs() []protocol.ConnectionRef {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]protocol.ConnectionRef, 0, len(r.participants)+len(r.peers))
	for _, p := range r.participants {
		out = append(out, protocol.ConnectionRef{
			ConnID:    p.ID,
			Transport: protocol.TransportWebSocket,
			Subject:   p.Subject,
		})
	}
	for _, p := range r.peers {
		out = append(out, protocol.ConnectionRef{
			ConnID:        p.ID,
			Transport:     protocol.TransportWebRTC,
			Subject:       p.Subject,
			Subscriptions: p.subscriptionState(),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Subject != out[j].Subject {
			return out[i].Subject < out[j].Subject
		}
		return out[i].ConnID < out[j].ConnID
	})
	return out
}

func (r *Room) snapshot(agentOnline bool) RoomInfo {
	r.mu.RLock()
	participants := make([]ParticipantInfo, 0, len(r.participants)+len(r.peers))
	for _, p := range r.participants {
		participants = append(participants, ParticipantInfo{
			ID:         p.ID,
			Subject:    p.Subject,
			Transport:  protocol.TransportWebSocket,
			RemoteAddr: p.RemoteAddr,
			UserAgent:  p.UserAgent,
			JoinedAt:   p.JoinedAt,
		})
	}
	for _, p := range r.peers {
		participants = append(participants, ParticipantInfo{
			ID:         p.ID,
			Subject:    p.Subject,
			Transport:  protocol.TransportWebRTC,
			Tracks:     p.publishedCount(),
			RemoteAddr: p.RemoteAddr,
			UserAgent:  p.UserAgent,
			JoinedAt:   p.JoinedAt,
		})
	}
	peerCount := len(r.peers)
	r.mu.RUnlock()
	sortParticipants(participants)

	return RoomInfo{
		ID:               r.ID,
		Name:             r.Name,
		Metadata:         cloneMetadata(r.Metadata),
		KeyID:            r.KeyID,
		KeyName:          r.KeyName,
		CreatedAt:        r.CreatedAt,
		MaxParticipants:  r.MaxParticipants,
		ParticipantCount: len(participants),
		Participants:     participants,
		AgentOnline:      agentOnline,
		RTCPeers:         peerCount,
		RTCSubscriptions: r.subscriptionCount(),
		Tracks:           r.publishedCount(),
		Messages:         r.messages.Load(),
		Bytes:            r.bytes.Load(),
		RTPPackets:       r.rtpPackets.Load(),
		RTPBytes:         r.rtpBytes.Load(),
		RTPDropped:       r.rtpDropped.Load(),
		LastActivity:     r.lastActivity(),
	}
}

func (r *Room) subscriptionCount() int {
	r.mu.RLock()
	peers := make([]*RTCPeer, 0, len(r.peers))
	for _, p := range r.peers {
		peers = append(peers, p)
	}
	r.mu.RUnlock()
	total := 0
	for _, p := range peers {
		total += p.subscriptionCount()
	}
	return total
}

func (r *Room) recordTraffic(n int64) {
	r.messages.Add(1)
	r.bytes.Add(n)
}

func (r *Room) recordRTP(n int64) {
	r.rtpPackets.Add(1)
	r.rtpBytes.Add(n)
}

func (r *Room) recordRTPDrop() {
	r.rtpDropped.Add(1)
}

func sortParticipants(items []ParticipantInfo) {
	for i := 1; i < len(items); i++ {
		for j := i; j > 0 && items[j].JoinedAt.Before(items[j-1].JoinedAt); j-- {
			items[j], items[j-1] = items[j-1], items[j]
		}
	}
}

type ParticipantInfo struct {
	ID         string    `json:"id"`
	Subject    string    `json:"subject"`
	Transport  string    `json:"transport"`
	Tracks     int       `json:"tracks,omitempty"`
	RemoteAddr string    `json:"remote_addr"`
	UserAgent  string    `json:"user_agent"`
	JoinedAt   time.Time `json:"joined_at"`
}

type RoomInfo struct {
	ID               string            `json:"id"`
	Name             string            `json:"name"`
	Metadata         json.RawMessage   `json:"metadata,omitempty"`
	KeyID            string            `json:"key_id"`
	KeyName          string            `json:"key_name"`
	CreatedAt        time.Time         `json:"created_at"`
	MaxParticipants  int               `json:"max_participants"`
	ParticipantCount int               `json:"participant_count"`
	Participants     []ParticipantInfo `json:"participants"`
	AgentOnline      bool              `json:"agent_online"`
	RTCPeers         int               `json:"rtc_peers"`
	RTCSubscriptions int               `json:"rtc_subscriptions"`
	Tracks           int               `json:"tracks"`
	Messages         int64             `json:"messages"`
	Bytes            int64             `json:"bytes"`
	RTPPackets       int64             `json:"rtp_packets"`
	RTPBytes         int64             `json:"rtp_bytes"`
	RTPDropped       int64             `json:"rtp_dropped"`
	LastActivity     time.Time         `json:"last_activity"`
}
