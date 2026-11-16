package hub

import (
	"sync"
	"sync/atomic"
	"time"
)

type Room struct {
	ID              string
	Name            string
	KeyID           string
	KeyName         string
	MaxParticipants int
	CreatedAt       time.Time

	mu           sync.RWMutex
	participants map[string]*Participant
	closed       bool

	messages atomic.Int64
	bytes    atomic.Int64
}

func (r *Room) addParticipant(p *Participant) *Error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return ErrRoomClosed
	}
	if len(r.participants) >= r.MaxParticipants {
		return ErrRoomFull
	}
	r.participants[p.ID] = p
	return nil
}

func (r *Room) removeParticipant(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.participants[id]; !ok {
		return false
	}
	delete(r.participants, id)
	return true
}

func (r *Room) participantCount() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.participants)
}

func (r *Room) isFull() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.participants) >= r.MaxParticipants
}

func (r *Room) isClosed() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.closed
}

// closeAndDetach marks the room closed, empties the participant map and
// returns the connections that still need to be closed.
func (r *Room) closeAndDetach() []*Participant {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	r.closed = true
	out := make([]*Participant, 0, len(r.participants))
	for _, p := range r.participants {
		out = append(out, p)
	}
	r.participants = make(map[string]*Participant)
	return out
}

func (r *Room) snapshot(agentOnline bool) RoomInfo {
	r.mu.RLock()
	participants := make([]ParticipantInfo, 0, len(r.participants))
	for _, p := range r.participants {
		participants = append(participants, ParticipantInfo{
			ID:         p.ID,
			Subject:    p.Subject,
			RemoteAddr: p.RemoteAddr,
			UserAgent:  p.UserAgent,
			JoinedAt:   p.JoinedAt,
		})
	}
	r.mu.RUnlock()
	sortParticipants(participants)

	return RoomInfo{
		ID:               r.ID,
		Name:             r.Name,
		KeyID:            r.KeyID,
		KeyName:          r.KeyName,
		CreatedAt:        r.CreatedAt,
		MaxParticipants:  r.MaxParticipants,
		ParticipantCount: len(participants),
		Participants:     participants,
		AgentOnline:      agentOnline,
		Messages:         r.messages.Load(),
		Bytes:            r.bytes.Load(),
	}
}

func (r *Room) recordTraffic(n int64) {
	r.messages.Add(1)
	r.bytes.Add(n)
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
	RemoteAddr string    `json:"remote_addr"`
	UserAgent  string    `json:"user_agent"`
	JoinedAt   time.Time `json:"joined_at"`
}

type RoomInfo struct {
	ID               string            `json:"id"`
	Name             string            `json:"name"`
	KeyID            string            `json:"key_id"`
	KeyName          string            `json:"key_name"`
	CreatedAt        time.Time         `json:"created_at"`
	MaxParticipants  int               `json:"max_participants"`
	ParticipantCount int               `json:"participant_count"`
	Participants     []ParticipantInfo `json:"participants"`
	AgentOnline      bool              `json:"agent_online"`
	Messages         int64             `json:"messages"`
	Bytes            int64             `json:"bytes"`
}
