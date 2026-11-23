package hub

import (
	"encoding/json"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"

	"xiaoyurepeater/internal/protocol"
)

func newTestPeerForState(t *testing.T) (*Hub, *Room, *RTCPeer) {
	t.Helper()
	h, err := New(Config{
		MaxRoomsPerKey:         10,
		DefaultMaxParticipants: 10,
		MaxParticipantsCap:     20,
		MaxFrameBytes:          1 << 20,
		AuthTimeout:            5 * time.Second,
		AgentSubscriptionQueue: 32,
		WebRTC: WebRTCConfig{
			Enabled:           true,
			UDPPortMin:        55000,
			UDPPortMax:        55099,
			MaxTracksPerPeer:  8,
			VideoQueuePackets: 64,
			AudioQueuePackets: 64,
			ConnectTimeout:    30 * time.Second,
			PendingICELimit:   32,
		},
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("new hub: %v", err)
	}
	t.Cleanup(h.Shutdown)

	room := &Room{
		ID:              "r_state",
		Name:            "State Test",
		KeyID:           "k_test",
		KeyName:         "Test",
		MaxParticipants: 10,
		CreatedAt:       time.Now().UTC(),
		participants:    make(map[string]*Participant),
		peers:           make(map[string]*RTCPeer),
		published:       make(map[string]*publishedTrack),
	}
	pc, err := h.rtcAPI.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatalf("new pc: %v", err)
	}
	t.Cleanup(func() { _ = pc.Close() })

	p := &RTCPeer{
		ID:       "p_test",
		Subject:  "u1",
		JoinedAt: time.Now().UTC(),
		hub:      h,
		room:     room,
		pc:       pc,
		out:      make(chan []byte, 32),
		cmds:     make(chan peerCommand, 32),
		done:     make(chan struct{}),
		state: peerState{
			all:       true,
			wanted:    make(map[string]struct{}),
			desiredAt: make(map[string]time.Time),
			outbound:  make(map[string]*downlink),
			retrySkip: make(map[string]time.Time),
		},
	}
	return h, room, p
}

func drainSignals(p *RTCPeer) []protocol.RTCSignal {
	var out []protocol.RTCSignal
	for {
		select {
		case raw := <-p.out:
			var sig protocol.RTCSignal
			if err := json.Unmarshal(raw, &sig); err == nil {
				out = append(out, sig)
			}
		default:
			return out
		}
	}
}

func TestRTCPeerGlareTimeoutAndICERestartNoRollback(t *testing.T) {
	_, _, p := newTestPeerForState(t)

	// 客户端初始化一个 recvonly transceiver 并发起首个 offer
	clientPC, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatalf("new client pc: %v", err)
	}
	defer clientPC.Close()
	if _, err := clientPC.AddTransceiverFromKind(
		webrtc.RTPCodecTypeAudio,
		webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly},
	); err != nil {
		t.Fatalf("client add transceiver: %v", err)
	}
	offer, err := clientPC.CreateOffer(nil)
	if err != nil {
		t.Fatalf("client create offer: %v", err)
	}
	if err := clientPC.SetLocalDescription(offer); err != nil {
		t.Fatalf("client set local offer: %v", err)
	}
	if err := p.onOffer(offer.SDP); err != nil {
		t.Fatalf("initial onOffer: %v", err)
	}
	sigs := drainSignals(p)
	if len(sigs) == 0 || sigs[0].Type != protocol.TypeAnswer {
		t.Fatalf("expected initial answer, got %+v", sigs)
	}
	if err := clientPC.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: sigs[0].SDP}); err != nil {
		t.Fatalf("client set answer: %v", err)
	}

	// 服务端加一条 sendonly 下行轨并进入 HaveLocalOffer
	local, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{
		MimeType:  webrtc.MimeTypeVP8,
		ClockRate: 90000,
	}, "t_v1", "cam")
	if err != nil {
		t.Fatalf("new local track: %v", err)
	}
	tr, err := p.pc.AddTransceiverFromTrack(local, webrtc.RTPTransceiverInit{
		Direction: webrtc.RTPTransceiverDirectionSendonly,
	})
	if err != nil {
		t.Fatalf("add sendonly transceiver: %v", err)
	}
	pubOwner := &RTCPeer{ID: "p_other"}
	pubOwner.closed.Store(true)
	pub := &publishedTrack{
		key:           "t_v1",
		room:          p.room,
		owner:         pubOwner,
		info:          protocol.RTCTrack{ID: "t_v1", Kind: "video", StreamID: "cam", Codec: webrtc.MimeTypeVP8, PublisherID: "p_other", Subject: "u2"},
		video:         true,
		keyframeProbe: newKeyframeProbe(webrtc.MimeTypeVP8),
		detectable:    true,
		subs:          make(map[*RTCPeer]*downlink),
	}
	dl := newDownlink(p, pub, local, tr, tr.Sender(), 16)
	p.state.outbound["t_v1"] = dl
	p.state.everTracks = true

	now := time.Now()
	p.state.needOffer = true
	p.tryNegotiate(now)
	if p.pc.SignalingState() != webrtc.SignalingStateHaveLocalOffer {
		t.Fatalf("expected HaveLocalOffer, got %s", p.pc.SignalingState())
	}
	sigs = drainSignals(p)
	if len(sigs) != 1 || sigs[0].Type != protocol.TypeOffer {
		t.Fatalf("expected server offer, got %+v", sigs)
	}
	if len(sigs[0].Tracks) != 1 || sigs[0].Tracks[0].MID == "" {
		t.Fatalf("expected offer tracks with non-empty MID, got %+v", sigs[0].Tracks)
	}

	// 1. 模拟 Glare：服务端处于 HaveLocalOffer 时客户端也发来 offer，不能因 rollback 报错断开
	if _, err := clientPC.AddTransceiverFromKind(webrtc.RTPCodecTypeVideo, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionSendonly}); err != nil {
		t.Fatalf("client add video: %v", err)
	}
	collidingOffer, err := clientPC.CreateOffer(nil)
	if err != nil {
		t.Fatalf("client colliding offer: %v", err)
	}
	if err := p.onOffer(collidingOffer.SDP); err != nil {
		t.Fatalf("colliding onOffer must not fail: %v", err)
	}
	if p.isClosed() || p.pc.SignalingState() != webrtc.SignalingStateHaveLocalOffer {
		t.Fatalf("peer closed=%v state=%s after glare", p.isClosed(), p.pc.SignalingState())
	}

	// 2. 模拟 offer 超时：tickOffer 只能退避重发当前 offer，不能 rollback 或断线
	p.tickOffer(now.Add(peerOfferTimeout + time.Millisecond))
	if p.isClosed() {
		t.Fatalf("tickOffer must not close peer on timeout")
	}
	resends := drainSignals(p)
	if len(resends) != 1 || resends[0].Type != protocol.TypeOffer || len(resends[0].Tracks) != 1 {
		t.Fatalf("expected resent offer with tracks, got %+v", resends)
	}

	// 3. 模拟断线期间触发 ICE 重启且仍在 HaveLocalOffer：同样不能 rollback 失败断开
	p.startICERestart(now.Add(peerOfferTimeout + 2*time.Second))
	if p.isClosed() || !p.state.restarting || !p.state.needOffer {
		t.Fatalf("startICERestart broke state: closed=%v restarting=%v needOffer=%v", p.isClosed(), p.state.restarting, p.state.needOffer)
	}
	_ = drainSignals(p)

	// 4. 校验未完成协商前 dl.negotiated 为 false，不会误清 waiting
	if dl.negotiated.Load() || !dl.waiting.Load() {
		t.Fatalf("expected negotiated=false waiting=true before answer")
	}

	// 5. 客户端最终应答服务端 offer，应恢复到 stable 并触发等待中的 ICE 重启 offer，同时标记 negotiated=true
	if err := clientPC.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: sigs[0].SDP}); err != nil {
		t.Fatalf("client set remote offer: %v", err)
	}
	ans, err := clientPC.CreateAnswer(nil)
	if err != nil {
		t.Fatalf("client create answer: %v", err)
	}
	if err := p.onAnswer(ans.SDP); err != nil {
		t.Fatalf("onAnswer failed: %v", err)
	}
	if !dl.negotiated.Load() {
		t.Fatalf("expected dl.negotiated=true after answer")
	}
	// pending needOffer(restarting) 会紧接着发出 ICE restart offer
	followups := drainSignals(p)
	if len(followups) != 1 || followups[0].Type != protocol.TypeOffer {
		t.Fatalf("expected followup ICE restart offer, got %+v", followups)
	}
	// 验证 rtp.Packet 依赖无未用报错
	_ = rtp.Packet{}
}
