package protocol

import (
	"encoding/json"
	"time"
)

const Version = 1

const (
	TypeHello        = "hello"
	TypeAuthRequest  = "auth_request"
	TypeAuthResponse = "auth_response"
	TypeUserJoined   = "user_joined"
	TypeUserLeft     = "user_left"
	TypeRoomClosed   = "room_closed"
	TypePing         = "ping"
	TypePong         = "pong"

	TypeOffer                = "offer"
	TypeAnswer               = "answer"
	TypeICE                  = "ice"
	TypeBye                  = "bye"
	TypeError                = "error"
	TypeTrackAdded           = "track_added"
	TypeTrackRemoved         = "track_removed"
	TypeTrackPublished       = "track_published"
	TypeTrackUnpublished     = "track_unpublished"
	TypeSubscriptions        = "subscriptions"
	TypeSubscriptionsUpdated = "subscriptions_updated"
	TypeDisconnect           = "disconnect"
	TypeDisconnected         = "disconnected"
	TypeRequestKeyframe      = "request_keyframe"
)

const (
	TransportWebSocket = "ws"
	TransportWebRTC    = "webrtc"
)

type RoomRef struct {
	ID           string          `json:"id"`
	Name         string          `json:"name"`
	Metadata     json.RawMessage `json:"metadata,omitempty"`
	Participants int             `json:"participants"` // 兼容字段，等于 Connections 数量
	CreatedAt    time.Time       `json:"created_at"`
	Connections  []ConnectionRef `json:"connections,omitempty"`
	Tracks       []RTCTrack      `json:"tracks,omitempty"`
}

// ConnectionRef is the neutral per-connection snapshot sent to the owning
// consumer after reconnect. It lets the consumer address existing WebRTC
// participants for subscription commands without exposing any payload.
// Subscriptions is the relay's stored desired state so a reconnecting
// consumer can reconcile without replaying every historical command.
type ConnectionRef struct {
	ConnID        string             `json:"conn_id"`
	Transport     string             `json:"transport"`
	Subject       string             `json:"subject"`
	Subscriptions *SubscriptionState `json:"subscriptions,omitempty"`
}

// SubscriptionState is the desired subscription of one WebRTC participant.
// Applied lists tracks currently attached to the PeerConnection; TrackIDs is
// the desired list and may contain tracks that are not published yet.
type SubscriptionState struct {
	All       bool      `json:"all"`
	TrackIDs  []string  `json:"track_ids"`
	Revision  int64     `json:"revision,omitempty"`
	Applied   []string  `json:"applied,omitempty"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
}

type Hello struct {
	Type       string    `json:"type"`
	Protocol   int       `json:"protocol"`
	AgentID    string    `json:"agent_id"`
	KeyID      string    `json:"key_id"`
	KeyName    string    `json:"key_name"`
	ServerTime time.Time `json:"server_time"`
	Rooms      []RoomRef `json:"rooms"`
	Limits     Limits    `json:"limits"`
	Features   []string  `json:"features,omitempty"`
}

type Limits struct {
	MaxRooms             int   `json:"max_rooms"`
	MaxFrameBytes        int64 `json:"max_frame_bytes"`
	AuthTimeoutMillis    int   `json:"auth_timeout_ms"`
	MaxParticipantsLimit int   `json:"max_participants_limit"`
	WebRTC               bool  `json:"webrtc"`
	MaxTracksPerPeer     int   `json:"max_tracks_per_peer"`
}

type AuthRequest struct {
	Type        string              `json:"type"`
	RequestID   string              `json:"request_id"`
	RoomID      string              `json:"room_id"`
	RoomName    string              `json:"room_name"`
	Metadata    json.RawMessage     `json:"metadata,omitempty"`
	Transport   string              `json:"transport"`
	RemoteAddr  string              `json:"remote_addr"`
	Query       map[string][]string `json:"query"`
	Headers     map[string][]string `json:"headers"`
	RequestedAt time.Time           `json:"requested_at"`
}

// AuthResponse is the consumer's verdict. Subject is only a display label
// for the console; the relay never interprets it or the frame payloads.
type AuthResponse struct {
	Type      string `json:"type"`
	RequestID string `json:"request_id"`
	Allow     *bool  `json:"allow"`
	Subject   string `json:"subject,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

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

type RoomClosed struct {
	Type     string    `json:"type"`
	RoomID   string    `json:"room_id"`
	RoomName string    `json:"room_name"`
	Reason   string    `json:"reason"`
	ClosedAt time.Time `json:"closed_at"`
}

type Ping struct {
	Type string    `json:"type"`
	Time time.Time `json:"time"`
}

type Pong struct {
	Type string    `json:"type"`
	Time time.Time `json:"time"`
}

// RTCSignal is the JSON envelope used only on the room signaling channel.
// SDP and ICE candidates are opaque to the relay; RTP payloads never pass
// through this channel.
type RTCSignal struct {
	Type      string        `json:"type"`
	SDP       string        `json:"sdp,omitempty"`
	Candidate *ICECandidate `json:"candidate,omitempty"`
	Track     *RTCTrack     `json:"track,omitempty"`
	Tracks    []RTCTrack    `json:"tracks,omitempty"`
	Error     string        `json:"error,omitempty"`
}

type ICECandidate struct {
	Candidate        string  `json:"candidate"`
	SDPMid           *string `json:"sdp_mid,omitempty"`
	SDPMLineIndex    *uint16 `json:"sdp_mline_index,omitempty"`
	UsernameFragment *string `json:"username_fragment,omitempty"`
}

// RTCTrack describes a published track without interpreting its payload.
// Codec and kind are passthrough metadata needed for SDP negotiation.
type RTCTrack struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	StreamID    string `json:"stream_id"`
	Codec       string `json:"codec"`
	RID         string `json:"rid,omitempty"`
	MID         string `json:"mid,omitempty"`
	PublisherID string `json:"publisher_id"`
	Subject     string `json:"subject,omitempty"`
}

// TrackPublished and TrackUnpublished are control events for the consumer.
// They describe room-level track metadata; no media bytes pass through the
// agent channel.
type TrackPublished struct {
	Type        string    `json:"type"`
	RoomID      string    `json:"room_id"`
	RoomName    string    `json:"room_name"`
	ConnID      string    `json:"conn_id"`
	Track       RTCTrack  `json:"track"`
	PublishedAt time.Time `json:"published_at"`
}

type TrackUnpublished struct {
	Type          string    `json:"type"`
	RoomID        string    `json:"room_id"`
	RoomName      string    `json:"room_name"`
	ConnID        string    `json:"conn_id"`
	Track         RTCTrack  `json:"track"`
	Reason        string    `json:"reason"`
	UnpublishedAt time.Time `json:"unpublished_at"`
}

// SubscriptionsCommand is sent by the consumer to set the complete
// subscription state of one WebRTC participant. If All is true, the relay
// forwards every current and future room track and ignores TrackIDs.
// Otherwise only the listed track IDs are forwarded; an empty list means none.
// Revision is optional; when present it must increase for newer commands so
// the relay can drop stale replays after a reconnect.
type SubscriptionsCommand struct {
	Type      string   `json:"type"`
	RequestID string   `json:"request_id"`
	RoomID    string   `json:"room_id"`
	ConnID    string   `json:"conn_id"`
	TrackIDs  []string `json:"track_ids"`
	All       bool     `json:"all"`
	Revision  int64    `json:"revision,omitempty"`
}

// SubscriptionsUpdate acknowledges a subscriptions command. Applied is the
// set of tracks currently attached, Pending the desired tracks that are not
// published or not synced yet. Duplicate/Stale mark idempotent replays.
type SubscriptionsUpdate struct {
	Type      string   `json:"type"`
	RequestID string   `json:"request_id"`
	RoomID    string   `json:"room_id"`
	ConnID    string   `json:"conn_id"`
	TrackIDs  []string `json:"track_ids"`
	All       bool     `json:"all"`
	Revision  int64    `json:"revision,omitempty"`
	Applied   []string `json:"applied,omitempty"`
	Pending   []string `json:"pending,omitempty"`
	Duplicate bool     `json:"duplicate,omitempty"`
	Stale     bool     `json:"stale,omitempty"`
}

type AgentError struct {
	Type      string `json:"type"`
	RequestID string `json:"request_id,omitempty"`
	RoomID    string `json:"room_id,omitempty"`
	ConnID    string `json:"conn_id,omitempty"`
	Error     string `json:"error"`
}

// DisconnectCommand asks the relay to close one specific room connection
// (either ws or webrtc) immediately, freeing its tracks and room slot.
type DisconnectCommand struct {
	Type      string `json:"type"`
	RequestID string `json:"request_id"`
	RoomID    string `json:"room_id"`
	ConnID    string `json:"conn_id"`
	Code      int    `json:"code,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

type DisconnectedResult struct {
	Type      string `json:"type"`
	RequestID string `json:"request_id"`
	RoomID    string `json:"room_id"`
	ConnID    string `json:"conn_id"`
	Closed    bool   `json:"closed"`
}
