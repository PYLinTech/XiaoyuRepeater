package protocol

import "time"

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

	TypeOffer        = "offer"
	TypeAnswer       = "answer"
	TypeICE          = "ice"
	TypeBye          = "bye"
	TypeError        = "error"
	TypeTrackAdded   = "track_added"
	TypeTrackRemoved = "track_removed"
)

const (
	TransportWebSocket = "ws"
	TransportWebRTC    = "webrtc"
)

type RoomRef struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Participants int       `json:"participants"`
	CreatedAt    time.Time `json:"created_at"`
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
	PublisherID string `json:"publisher_id"`
	Subject     string `json:"subject,omitempty"`
}
