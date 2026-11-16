package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"

	"xiaoyurepeater/internal/config"
	"xiaoyurepeater/internal/hub"
	"xiaoyurepeater/internal/protocol"
)

type rtcClient struct {
	t    *testing.T
	name string
	ws   *websocket.Conn
	pc   *webrtc.PeerConnection

	writeMu sync.Mutex

	iceMu      sync.Mutex
	remoteSet  bool
	pendingICE []webrtc.ICECandidateInit

	offers     chan string
	answers    chan string
	errors     chan string
	tracks     chan *webrtc.TrackRemote
	removed    chan protocol.RTCTrack
	closeErr   chan error
	connected  chan struct{}
	connectOne sync.Once

	done chan struct{}
	once sync.Once
}

func dialRTC(t *testing.T, tsURL, roomID, token string) (*rtcClient, int, []byte) {
	t.Helper()
	target := fmt.Sprintf("%s/v1/rooms/%s/webrtc?token=%s&name=%s",
		wsBaseURL(tsURL), roomID, url.QueryEscape(token), url.QueryEscape(token))
	ws, resp, err := websocket.DefaultDialer.Dial(target, nil)
	if err != nil {
		status := 0
		var body []byte
		if resp != nil {
			status = resp.StatusCode
			body, _ = readLimited(resp)
		}
		return nil, status, body
	}
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		_ = ws.Close()
		t.Fatalf("new peer connection: %v", err)
	}
	c := &rtcClient{
		t:         t,
		name:      token,
		ws:        ws,
		pc:        pc,
		offers:    make(chan string, 16),
		answers:   make(chan string, 16),
		errors:    make(chan string, 16),
		tracks:    make(chan *webrtc.TrackRemote, 8),
		removed:   make(chan protocol.RTCTrack, 8),
		closeErr:  make(chan error, 1),
		connected: make(chan struct{}),
		done:      make(chan struct{}),
	}
	if got := resp.Header.Get("X-Repeater-Transport"); got != protocol.TransportWebRTC {
		t.Fatalf("transport header = %q", got)
	}

	pc.OnICECandidate(func(candidate *webrtc.ICECandidate) {
		if candidate == nil {
			return
		}
		init := candidate.ToJSON()
		c.send(protocol.RTCSignal{
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
		if state == webrtc.PeerConnectionStateConnected {
			c.connectOne.Do(func() { close(c.connected) })
		}
	})
	pc.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		select {
		case c.tracks <- track:
		default:
		}
	})
	go c.readLoop()
	t.Cleanup(c.close)
	return c, resp.StatusCode, nil
}

func mustDialRTC(t *testing.T, tsURL, roomID, token string) *rtcClient {
	t.Helper()
	c, status, body := dialRTC(t, tsURL, roomID, token)
	if c == nil {
		t.Fatalf("dial rtc %q: status=%d body=%s", token, status, body)
	}
	return c
}

func readLimited(resp *http.Response) ([]byte, error) {
	defer resp.Body.Close()
	buf := make([]byte, 4096)
	n, _ := resp.Body.Read(buf)
	return buf[:n], nil
}

func (c *rtcClient) close() {
	c.once.Do(func() {
		_ = c.pc.Close()
		_ = c.ws.Close()
		close(c.done)
	})
}

func (c *rtcClient) readLoop() {
	for {
		_, raw, err := c.ws.ReadMessage()
		if err != nil {
			select {
			case c.closeErr <- err:
			default:
			}
			return
		}
		var msg protocol.RTCSignal
		if err := json.Unmarshal(raw, &msg); err != nil {
			continue
		}
		switch msg.Type {
		case protocol.TypeOffer:
			select {
			case c.offers <- msg.SDP:
			case <-c.done:
				return
			}
		case protocol.TypeAnswer:
			select {
			case c.answers <- msg.SDP:
			case <-c.done:
				return
			}
		case protocol.TypeICE:
			if msg.Candidate != nil {
				c.addICE(msg.Candidate)
			}
		case protocol.TypeTrackRemoved:
			if msg.Track != nil {
				select {
				case c.removed <- *msg.Track:
				default:
				}
			}
		case protocol.TypeError:
			select {
			case c.errors <- msg.Error:
			default:
			}
		}
	}
}

func (c *rtcClient) send(msg protocol.RTCSignal) {
	raw, err := json.Marshal(msg)
	if err != nil {
		return
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_ = c.ws.SetWriteDeadline(time.Now().Add(3 * time.Second))
	_ = c.ws.WriteMessage(websocket.TextMessage, raw)
}

func (c *rtcClient) addICE(candidate *protocol.ICECandidate) {
	init := webrtc.ICECandidateInit{
		Candidate:        candidate.Candidate,
		SDPMid:           candidate.SDPMid,
		SDPMLineIndex:    candidate.SDPMLineIndex,
		UsernameFragment: candidate.UsernameFragment,
	}
	c.iceMu.Lock()
	if !c.remoteSet {
		c.pendingICE = append(c.pendingICE, init)
		c.iceMu.Unlock()
		return
	}
	c.iceMu.Unlock()
	_ = c.pc.AddICECandidate(init)
}

func (c *rtcClient) setRemote(sdp string, kind webrtc.SDPType) error {
	if err := c.pc.SetRemoteDescription(webrtc.SessionDescription{Type: kind, SDP: sdp}); err != nil {
		return err
	}
	c.iceMu.Lock()
	c.remoteSet = true
	pending := c.pendingICE
	c.pendingICE = nil
	c.iceMu.Unlock()
	for _, candidate := range pending {
		_ = c.pc.AddICECandidate(candidate)
	}
	return nil
}

func (c *rtcClient) sendOffer() {
	c.t.Helper()
	offer, err := c.pc.CreateOffer(nil)
	if err != nil {
		c.t.Fatalf("create offer: %v", err)
	}
	if err := c.pc.SetLocalDescription(offer); err != nil {
		c.t.Fatalf("set local offer: %v", err)
	}
	c.send(protocol.RTCSignal{Type: protocol.TypeOffer, SDP: offer.SDP})
}

func (c *rtcClient) waitAnswer(timeout time.Duration) {
	c.t.Helper()
	select {
	case sdp := <-c.answers:
		if err := c.setRemote(sdp, webrtc.SDPTypeAnswer); err != nil {
			c.t.Fatalf("%s: set remote answer: %v", c.name, err)
		}
	case msg := <-c.errors:
		c.t.Fatalf("%s: server signaling error: %s", c.name, msg)
	case <-time.After(timeout):
		c.t.Fatalf("%s: timed out waiting for answer", c.name)
	}
}

func (c *rtcClient) waitOffer(timeout time.Duration) string {
	c.t.Helper()
	select {
	case sdp := <-c.offers:
		return sdp
	case msg := <-c.errors:
		c.t.Fatalf("%s: server signaling error: %s", c.name, msg)
	case <-time.After(timeout):
		c.t.Fatalf("%s: timed out waiting for server offer", c.name)
	}
	return ""
}

func (c *rtcClient) answerOffer(sdp string) {
	c.t.Helper()
	if err := c.setRemote(sdp, webrtc.SDPTypeOffer); err != nil {
		c.t.Fatalf("%s: set remote offer: %v", c.name, err)
	}
	answer, err := c.pc.CreateAnswer(nil)
	if err != nil {
		c.t.Fatalf("%s: create answer: %v", c.name, err)
	}
	if err := c.pc.SetLocalDescription(answer); err != nil {
		c.t.Fatalf("%s: set local answer: %v", c.name, err)
	}
	c.send(protocol.RTCSignal{Type: protocol.TypeAnswer, SDP: answer.SDP})
}

func (c *rtcClient) waitConnected(timeout time.Duration) {
	c.t.Helper()
	select {
	case <-c.connected:
	case msg := <-c.errors:
		c.t.Fatalf("%s: server signaling error: %s", c.name, msg)
	case <-time.After(timeout):
		c.t.Fatalf("%s: timed out waiting for WebRTC connection", c.name)
	}
}

func (c *rtcClient) waitCloseCode(code int, timeout time.Duration) {
	c.t.Helper()
	select {
	case err := <-c.closeErr:
		if !websocket.IsCloseError(err, code) {
			c.t.Fatalf("%s: close error = %v, want code %d", c.name, err, code)
		}
	case <-time.After(timeout):
		c.t.Fatalf("%s: timed out waiting for close %d", c.name, code)
	}
}

func opusTrack(t *testing.T) *webrtc.TrackLocalStaticRTP {
	t.Helper()
	track, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{
		MimeType:  webrtc.MimeTypeOpus,
		ClockRate: 48000,
		Channels:  2,
	}, "audio", "microphone")
	if err != nil {
		t.Fatalf("new opus track: %v", err)
	}
	return track
}

func TestWebRTCSFURelaysRTP(t *testing.T) {
	env := newTestEnv(t, func(cfg *config.Config) {
		cfg.WebRTCEnabled = true
		cfg.WebRTCUDPPortMin = 54000
		cfg.WebRTCUDPPortMax = 54099
		cfg.WebRTCMaxTracksPerPeer = 4
	})
	agent, _ := newAgentHarness(t, env.ts.URL, env.secret, allowTokens("ok-"))
	room := env.createRoom("WebRTC 房间", 2)

	publisher := mustDialRTC(t, env.ts.URL, room.ID, "ok-publisher")
	event := agent.waitEvent(t, protocol.TypeAuthRequest, 2*time.Second)
	if event["transport"] != protocol.TransportWebRTC {
		t.Fatalf("auth transport = %v", event["transport"])
	}
	publishTrack := opusTrack(t)
	if _, err := publisher.pc.AddTrack(publishTrack); err != nil {
		t.Fatalf("add publisher track: %v", err)
	}
	publisher.sendOffer()
	publisher.waitAnswer(15 * time.Second)
	publisher.waitConnected(20 * time.Second)

	if _, status, body := dialRTC(t, env.ts.URL, room.ID, "deny-me"); status != http.StatusForbidden {
		t.Fatalf("denied rtc status=%d body=%s", status, body)
	}

	payload := []byte{0xde, 0xad, 0xbe, 0xef, 0x01, 0x02, 0x03}
	stopWriter := make(chan struct{})
	go func() {
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		seq := uint16(1)
		for timestamp := uint32(0); ; timestamp += 960 {
			select {
			case <-stopWriter:
				return
			case <-ticker.C:
				_ = publishTrack.WriteRTP(&rtp.Packet{
					Header: rtp.Header{
						Version:        2,
						PayloadType:    111,
						SequenceNumber: seq,
						Timestamp:      timestamp,
						SSRC:           0x13572468,
						Marker:         true,
					},
					Payload: payload,
				})
				seq++
			}
		}
	}()
	defer close(stopWriter)

	time.Sleep(200 * time.Millisecond)

	subscriber := mustDialRTC(t, env.ts.URL, room.ID, "ok-subscriber")
	if _, err := subscriber.pc.AddTransceiverFromKind(
		webrtc.RTPCodecTypeAudio,
		webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly},
	); err != nil {
		t.Fatalf("add recvonly transceiver: %v", err)
	}
	subscriber.sendOffer()
	subscriber.waitAnswer(15 * time.Second)
	serverOffer := subscriber.waitOffer(15 * time.Second)
	subscriber.answerOffer(serverOffer)
	subscriber.waitConnected(20 * time.Second)

	var remote *webrtc.TrackRemote
	select {
	case remote = <-subscriber.tracks:
	case <-time.After(15 * time.Second):
		t.Fatalf("timed out waiting for subscriber track")
	}
	_ = remote.SetReadDeadline(time.Now().Add(15 * time.Second))
	received := false
	for i := 0; i < 20; i++ {
		pkt, _, err := remote.ReadRTP()
		if err != nil {
			break
		}
		if bytes.Equal(pkt.Payload, payload) {
			received = true
			break
		}
	}
	if !received {
		t.Fatalf("subscriber did not receive the original RTP payload")
	}
	select {
	case track := <-publisher.tracks:
		t.Fatalf("publisher received its own track %q", track.ID())
	case <-time.After(time.Second):
	}

	if _, status, body := dialRTC(t, env.ts.URL, room.ID, "ok-third"); status != http.StatusConflict {
		t.Fatalf("third rtc peer status=%d body=%s", status, body)
	}

	var overview struct {
		Stats hub.StatsSnapshot `json:"stats"`
		Rooms []hub.RoomInfo    `json:"rooms"`
	}
	status, body := env.adminJSON(http.MethodGet, "/api/admin/overview", nil, &overview)
	if status != http.StatusOK {
		t.Fatalf("overview status=%d body=%s", status, body)
	}
	if overview.Stats.Participants != 2 || overview.Stats.RTCPeers != 2 || overview.Stats.RTCTracks != 1 {
		t.Fatalf("unexpected rtc stats: %+v", overview.Stats)
	}
	if overview.Stats.RTPPackets == 0 || overview.Stats.RTPBytes == 0 {
		t.Fatalf("no rtp traffic recorded: %+v", overview.Stats)
	}
	if len(overview.Rooms) != 1 || overview.Rooms[0].Tracks != 1 || overview.Rooms[0].RTCPeers != 2 {
		t.Fatalf("unexpected room snapshot: %+v", overview.Rooms)
	}
	transports := map[string]int{}
	for _, participant := range overview.Rooms[0].Participants {
		transports[participant.Transport]++
	}
	if transports[protocol.TransportWebRTC] != 2 {
		t.Fatalf("participant transports = %+v", transports)
	}

	status, body = env.adminJSON(http.MethodDelete, "/api/admin/rooms/"+room.ID, nil, nil)
	if status != http.StatusOK {
		t.Fatalf("close room status=%d body=%s", status, body)
	}
	subscriber.waitCloseCode(4000, 10*time.Second)
	agent.waitEvent(t, protocol.TypeRoomClosed, 5*time.Second)
}

func TestWebRTCTrackRemoval(t *testing.T) {
	env := newTestEnv(t, func(cfg *config.Config) {
		cfg.WebRTCEnabled = true
		cfg.WebRTCUDPPortMin = 54000
		cfg.WebRTCUDPPortMax = 54099
		cfg.WebRTCMaxTracksPerPeer = 4
	})
	agent, _ := newAgentHarness(t, env.ts.URL, env.secret, allowTokens("ok-"))
	room := env.createRoom("WebRTC 移除轨道", 2)

	publisher := mustDialRTC(t, env.ts.URL, room.ID, "ok-publisher")
	agent.waitEvent(t, protocol.TypeAuthRequest, 5*time.Second)
	publishTrack := opusTrack(t)
	sender, err := publisher.pc.AddTrack(publishTrack)
	if err != nil {
		t.Fatalf("add publisher track: %v", err)
	}
	publisher.sendOffer()
	publisher.waitAnswer(15 * time.Second)
	publisher.waitConnected(20 * time.Second)
	stopWriter := make(chan struct{})
	defer close(stopWriter)
	go func() {
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		for seq := uint16(1); ; seq++ {
			select {
			case <-stopWriter:
				return
			case <-ticker.C:
				_ = publishTrack.WriteRTP(&rtp.Packet{
					Header: rtp.Header{
						Version:        2,
						PayloadType:    111,
						SequenceNumber: seq,
						Timestamp:      uint32(seq) * 960,
						SSRC:           0x13572468,
						Marker:         true,
					},
					Payload: []byte{0xde, 0xad, 0xbe, 0xef},
				})
			}
		}
	}()
	deadline := time.Now().Add(10 * time.Second)
	for {
		var published struct {
			Stats hub.StatsSnapshot `json:"stats"`
		}
		status, body := env.adminJSON(http.MethodGet, "/api/admin/overview", nil, &published)
		if status != http.StatusOK {
			t.Fatalf("overview status=%d body=%s", status, body)
		}
		if published.Stats.RTCTracks == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("track was not published, stats=%+v", published.Stats)
		}
		time.Sleep(50 * time.Millisecond)
	}

	subscriber := mustDialRTC(t, env.ts.URL, room.ID, "ok-subscriber")
	if _, err := subscriber.pc.AddTransceiverFromKind(
		webrtc.RTPCodecTypeAudio,
		webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly},
	); err != nil {
		t.Fatalf("add recvonly transceiver: %v", err)
	}
	subscriber.sendOffer()
	subscriber.waitAnswer(15 * time.Second)
	subscriber.answerOffer(subscriber.waitOffer(15 * time.Second))
	subscriber.waitConnected(20 * time.Second)
	select {
	case <-subscriber.tracks:
	case <-time.After(15 * time.Second):
		t.Fatalf("timed out waiting for subscriber track")
	}

	if err := publisher.pc.RemoveTrack(sender); err != nil {
		t.Fatalf("remove publisher track: %v", err)
	}
	publisher.sendOffer()
	publisher.waitAnswer(15 * time.Second)

	select {
	case info := <-subscriber.removed:
		if info.Kind != "audio" {
			t.Fatalf("removed track kind = %q", info.Kind)
		}
	case <-time.After(20 * time.Second):
		t.Fatalf("timed out waiting for track_removed")
	}

	var overview struct {
		Stats hub.StatsSnapshot `json:"stats"`
	}
	status, body := env.adminJSON(http.MethodGet, "/api/admin/overview", nil, &overview)
	if status != http.StatusOK {
		t.Fatalf("overview status=%d body=%s", status, body)
	}
	if overview.Stats.RTCTracks != 0 {
		t.Fatalf("rtc tracks after removal = %d", overview.Stats.RTCTracks)
	}

	status, body = env.adminJSON(http.MethodDelete, "/api/admin/rooms/"+room.ID, nil, nil)
	if status != http.StatusOK {
		t.Fatalf("close room status=%d body=%s", status, body)
	}
	agent.waitEvent(t, protocol.TypeRoomClosed, 5*time.Second)
}

func TestWebRTCDisabled(t *testing.T) {
	env := newTestEnv(t, nil)
	agent, _ := newAgentHarness(t, env.ts.URL, env.secret, allowTokens("ok-"))

	var created struct {
		WebRTCPath string       `json:"webrtc_path"`
		WebRTCURL  string       `json:"webrtc_url"`
		Room       hub.RoomInfo `json:"room"`
	}
	status, body := env.consumerJSON(http.MethodPost, "/v1/rooms", map[string]any{
		"name":             "无媒体房间",
		"max_participants": 2,
	}, &created)
	if status != http.StatusCreated {
		t.Fatalf("create room status=%d body=%s", status, body)
	}
	if created.WebRTCPath != "" || created.WebRTCURL != "" {
		t.Fatalf("disabled webrtc advertised: %+v", created)
	}

	_, status, body = dialRTC(t, env.ts.URL, created.Room.ID, "ok-alice")
	if status != http.StatusServiceUnavailable {
		t.Fatalf("disabled webrtc status=%d body=%s", status, body)
	}
	select {
	case event := <-agent.events:
		t.Fatalf("disabled webrtc triggered agent event: %v", event)
	case <-time.After(300 * time.Millisecond):
	}
}
