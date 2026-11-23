package hub

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
)

const (
	dropLogInterval = 5 * time.Second
)

// downlink is one subscription: a per-subscriber queue plus a writer that
// copies RTP into the subscriber's TrackLocalStaticRTP. Publishing never
// blocks on a slow subscriber; the queue is bounded and applies a per-kind
// drop policy instead.
type downlink struct {
	peer       *RTCPeer
	pub        *publishedTrack
	local      *webrtc.TrackLocalStaticRTP
	sender     *webrtc.RTPSender
	video      bool
	detectable bool

	queue     chan []byte
	done      chan struct{}
	closeOnce sync.Once

	dropped     atomic.Int64
	waiting     atomic.Bool
	stopped     atomic.Bool
	pliIssued   atomic.Bool
	pliReset    atomic.Bool
	lastDropLog atomic.Int64

	// Scheduling state is only touched by the owner peer's actor goroutine.
	pliDue     time.Time
	pliBackoff time.Duration
}

func newDownlink(p *RTCPeer, pub *publishedTrack, local *webrtc.TrackLocalStaticRTP, sender *webrtc.RTPSender, queueSize int) *downlink {
	video := pub.info.Kind == "video"
	d := &downlink{
		peer:   p,
		pub:    pub,
		local:  local,
		sender: sender,
		video:  video,
		queue:  make(chan []byte, queueSize),
		done:   make(chan struct{}),
	}
	if video {
		d.detectable = pub.keyframeProbe.detectable()
		d.waiting.Store(true)
	}
	return d
}

func (d *downlink) close() {
	d.closeOnce.Do(func() {
		close(d.done)
	})
}

func (d *downlink) isClosed() bool {
	select {
	case <-d.done:
		return true
	default:
		return false
	}
}

func (d *downlink) markKeyframeNeeded() {
	if !d.video {
		return
	}
	d.waiting.Store(true)
	d.pliReset.Store(true)
}

func (d *downlink) clearKeyframeWait() {
	if d.video && d.waiting.Load() {
		d.waiting.Store(false)
	}
}

// enqueue never blocks the publisher. Video overflow discards the oldest
// packet and schedules a fresh keyframe; audio overflow is counted and only
// happens once the (larger) audio queue is genuinely full.
func (d *downlink) enqueue(raw []byte) {
	if d.stopped.Load() || d.isClosed() {
		return
	}
	select {
	case <-d.peer.done:
		return
	default:
	}
	select {
	case d.queue <- raw:
		d.peer.hub.rtpQueueDepth.Add(1)
		// 写入方已退出时不能留下无人消费的包，回收自身入队的那一条
		if d.stopped.Load() || d.isClosed() {
			select {
			case <-d.queue:
				d.peer.hub.rtpQueueDepth.Add(-1)
			default:
			}
		}
		return
	default:
	}
	select {
	case <-d.queue:
		d.peer.hub.rtpQueueDepth.Add(-1)
		d.noteDrop()
	default:
	}
	select {
	case d.queue <- raw:
		d.peer.hub.rtpQueueDepth.Add(1)
	default:
		d.noteDrop()
	}
}

func (d *downlink) noteDrop() {
	d.dropped.Add(1)
	d.peer.hub.rtpQueueDropped.Add(1)
	d.peer.room.recordRTPDrop()
	if d.video {
		d.waiting.Store(true)
		d.pliReset.Store(true)
	}
	now := time.Now().UnixMilli()
	last := d.lastDropLog.Load()
	if now-last >= dropLogInterval.Milliseconds() && d.lastDropLog.CompareAndSwap(last, now) {
		d.peer.hub.log.Warn("WebRTC 订阅队列溢出，丢弃旧数据",
			"room_id", d.peer.room.ID, "peer_id", d.peer.ID,
			"track_id", d.pub.key, "kind", d.pub.info.Kind, "dropped", d.dropped.Load())
	}
}

func (d *downlink) run() {
	defer func() {
		d.stopped.Store(true)
		d.drainQueue()
	}()
	var packet rtp.Packet
	for {
		select {
		case <-d.done:
			return
		case <-d.peer.done:
			return
		case raw := <-d.queue:
			d.peer.hub.rtpQueueDepth.Add(-1)
			if err := packet.Unmarshal(raw); err != nil {
				continue
			}
			if err := d.local.WriteRTP(&packet); err != nil {
				d.peer.postReliable(peerCommand{kind: cmdDownlinkFailed, key: d.pub.key})
				return
			}
			if d.video && !d.detectable && d.pliIssued.Swap(false) {
				d.waiting.Store(false)
			}
		}
	}
}

func (d *downlink) drainQueue() {
	for {
		select {
		case <-d.queue:
			d.peer.hub.rtpQueueDepth.Add(-1)
		default:
			return
		}
	}
}
