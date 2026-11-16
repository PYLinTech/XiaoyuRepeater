package xiaoyurepeater

import "time"

const (
	// Version is this SDK package version.
	Version = "1"
	// ProtocolVersion is the relay control protocol understood by this SDK.
	ProtocolVersion = 1
)

// RoomOptions controls room creation. Zero values are omitted so the relay
// applies its configured defaults.
type RoomOptions struct {
	Name            string `json:"name,omitempty"`
	MaxParticipants int    `json:"max_participants,omitempty"`
}

// VersionInfo describes the SDK package served by a relay instance.
type VersionInfo struct {
	Version      string   `json:"version"`
	Protocol     int      `json:"protocol"`
	Filename     string   `json:"filename"`
	DownloadPath string   `json:"download_path"`
	Languages    []string `json:"languages"`
}

// Participant is an online user or WebRTC peer in a room.
type Participant struct {
	ID         string    `json:"id"`
	Subject    string    `json:"subject"`
	Transport  string    `json:"transport"`
	Tracks     int       `json:"tracks,omitempty"`
	RemoteAddr string    `json:"remote_addr"`
	UserAgent  string    `json:"user_agent"`
	JoinedAt   time.Time `json:"joined_at"`
}

// Room is the live room state returned by the consumer API.
type Room struct {
	ID               string        `json:"id"`
	Name             string        `json:"name"`
	KeyID            string        `json:"key_id"`
	KeyName          string        `json:"key_name"`
	CreatedAt        time.Time     `json:"created_at"`
	MaxParticipants  int           `json:"max_participants"`
	ParticipantCount int           `json:"participant_count"`
	Participants     []Participant `json:"participants"`
	AgentOnline      bool          `json:"agent_online"`
	RTCPeers         int           `json:"rtc_peers"`
	Tracks           int           `json:"tracks"`
	Messages         int64         `json:"messages"`
	Bytes            int64         `json:"bytes"`
	RTPPackets       int64         `json:"rtp_packets"`
	RTPBytes         int64         `json:"rtp_bytes"`
}

// CreatedRoom is the response to room creation.
type CreatedRoom struct {
	Protocol   int    `json:"protocol"`
	Room       Room   `json:"room"`
	WSPath     string `json:"ws_path"`
	WSURL      string `json:"ws_url"`
	WebRTCPath string `json:"webrtc_path"`
	WebRTCURL  string `json:"webrtc_url"`
}

// ConnectionRef is an online connection snapshot sent in hello.
type ConnectionRef struct {
	ConnID    string `json:"conn_id"`
	Transport string `json:"transport"`
	Subject   string `json:"subject"`
}

// Track is room-level track metadata. Payload bytes never pass through the
// control channel.
type Track struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	StreamID    string `json:"stream_id"`
	Codec       string `json:"codec"`
	RID         string `json:"rid,omitempty"`
	PublisherID string `json:"publisher_id"`
	Subject     string `json:"subject,omitempty"`
}

// RoomSnapshot is the per-room state in hello.
type RoomSnapshot struct {
	ID           string          `json:"id"`
	Name         string          `json:"name"`
	Participants int             `json:"participants"`
	CreatedAt    time.Time       `json:"created_at"`
	Connections  []ConnectionRef `json:"connections,omitempty"`
	Tracks       []Track         `json:"tracks,omitempty"`
}

// Limits are the relay limits reported in hello.
type Limits struct {
	MaxRooms             int   `json:"max_rooms"`
	MaxFrameBytes        int64 `json:"max_frame_bytes"`
	AuthTimeoutMillis    int   `json:"auth_timeout_ms"`
	MaxParticipantsLimit int   `json:"max_participants_limit"`
	WebRTC               bool  `json:"webrtc"`
	MaxTracksPerPeer     int   `json:"max_tracks_per_peer"`
}

// Hello is the first message on every agent connection. It contains a full
// state snapshot so a reconnecting consumer can restore its own state.
type Hello struct {
	Type       string         `json:"type"`
	Protocol   int            `json:"protocol"`
	AgentID    string         `json:"agent_id"`
	KeyID      string         `json:"key_id"`
	KeyName    string         `json:"key_name"`
	ServerTime time.Time      `json:"server_time"`
	Rooms      []RoomSnapshot `json:"rooms"`
	Limits     Limits         `json:"limits"`
}

// AuthRequest is forwarded by the relay when a user wants to join a room.
// Query and Headers contain the user's handshake values; the relay does not
// interpret them, and always removes Cookie and WebSocket handshake headers.
type AuthRequest struct {
	Type        string              `json:"type"`
	RequestID   string              `json:"request_id"`
	RoomID      string              `json:"room_id"`
	RoomName    string              `json:"room_name"`
	Transport   string              `json:"transport"`
	RemoteAddr  string              `json:"remote_addr"`
	Query       map[string][]string `json:"query"`
	Headers     map[string][]string `json:"headers"`
	RequestedAt time.Time           `json:"requested_at"`
}

// AuthDecision is the consumer's verdict for one join request. Subject is only
// a console display label and is never used for routing or permissions.
type AuthDecision struct {
	Allow   bool
	Subject string
	Reason  string
}

// Allow returns an allowing decision with an optional display subject.
func Allow(subject string) AuthDecision {
	return AuthDecision{Allow: true, Subject: subject}
}

// Deny returns a rejecting decision. Reason is included in the user's 403.
func Deny(reason string) AuthDecision {
	return AuthDecision{Allow: false, Reason: reason}
}

// AuthResponse is the wire message sent back to the relay.
type AuthResponse struct {
	Type      string `json:"type"`
	RequestID string `json:"request_id"`
	Allow     *bool  `json:"allow"`
	Subject   string `json:"subject,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

// UserJoined is sent after a user passes authorization and joins.
type UserJoined struct {
	Type       string    `json:"type"`
	RoomID     string    `json:"room_id"`
	RoomName   string    `json:"room_name"`
	ConnID     string    `json:"conn_id"`
	Transport  string    `json:"transport"`
	Subject    string    `json:"subject"`
	RemoteAddr string    `json:"remote_addr"`
	JoinedAt   time.Time `json:"joined_at"`
}

// UserLeft is sent when a user or WebRTC peer leaves.
type UserLeft struct {
	Type      string    `json:"type"`
	RoomID    string    `json:"room_id"`
	RoomName  string    `json:"room_name"`
	ConnID    string    `json:"conn_id"`
	Transport string    `json:"transport"`
	Subject   string    `json:"subject"`
	Reason    string    `json:"reason"`
	LeftAt    time.Time `json:"left_at"`
}

// RoomClosed is sent when a room is closed.
type RoomClosed struct {
	Type     string    `json:"type"`
	RoomID   string    `json:"room_id"`
	RoomName string    `json:"room_name"`
	Reason   string    `json:"reason"`
	ClosedAt time.Time `json:"closed_at"`
}

// TrackPublished announces a new room-level track.
type TrackPublished struct {
	Type        string    `json:"type"`
	RoomID      string    `json:"room_id"`
	RoomName    string    `json:"room_name"`
	ConnID      string    `json:"conn_id"`
	Track       Track     `json:"track"`
	PublishedAt time.Time `json:"published_at"`
}

// TrackUnpublished announces that a track ended.
type TrackUnpublished struct {
	Type          string    `json:"type"`
	RoomID        string    `json:"room_id"`
	RoomName      string    `json:"room_name"`
	ConnID        string    `json:"conn_id"`
	Track         Track     `json:"track"`
	Reason        string    `json:"reason"`
	UnpublishedAt time.Time `json:"unpublished_at"`
}

// SubscriptionsCommand sets the complete subscription list of one WebRTC peer.
type SubscriptionsCommand struct {
	Type      string   `json:"type"`
	RequestID string   `json:"request_id"`
	RoomID    string   `json:"room_id"`
	ConnID    string   `json:"conn_id"`
	TrackIDs  []string `json:"track_ids"`
	All       bool     `json:"all"`
}

// SubscriptionsUpdate is the relay's confirmation of a subscription change.
type SubscriptionsUpdate struct {
	Type      string   `json:"type"`
	RequestID string   `json:"request_id"`
	RoomID    string   `json:"room_id"`
	ConnID    string   `json:"conn_id"`
	TrackIDs  []string `json:"track_ids"`
	All       bool     `json:"all"`
}

// AgentError is a command error returned on the control channel.
type AgentError struct {
	Type      string `json:"type"`
	RequestID string `json:"request_id,omitempty"`
	RoomID    string `json:"room_id,omitempty"`
	ConnID    string `json:"conn_id,omitempty"`
	Error     string `json:"error"`
}
