package hub

import (
	"strings"

	"github.com/pion/rtp/codecs"
)

// keyframeProbe detects video keyframes so the relay can stop requesting
// PLIs once a subscriber has recovered, without parsing whole frames.
type keyframeProbe struct {
	mime string
	vp8  codecs.VP8Packet
	vp9  codecs.VP9Packet
}

func newKeyframeProbe(mimeType string) *keyframeProbe {
	return &keyframeProbe{mime: strings.ToLower(mimeType)}
}

// detectable reports whether the probe can recognise keyframes for the codec.
func (k *keyframeProbe) detectable() bool {
	return strings.Contains(k.mime, "vp8") ||
		strings.Contains(k.mime, "vp9") ||
		strings.Contains(k.mime, "h264")
}

func (k *keyframeProbe) isKeyframe(payload []byte) bool {
	if len(payload) == 0 {
		return false
	}
	switch {
	case strings.Contains(k.mime, "vp8"):
		rest, err := k.vp8.Unmarshal(payload)
		if err != nil || k.vp8.S == 0 || len(rest) == 0 {
			return false
		}
		return rest[0]&0x01 == 0
	case strings.Contains(k.mime, "vp9"):
		rest, err := k.vp9.Unmarshal(payload)
		if err != nil || !k.vp9.B || len(rest) == 0 {
			return false
		}
		return (rest[0]>>2)&0x01 == 0
	case strings.Contains(k.mime, "h264"):
		return h264Keyframe(payload)
	default:
		return false
	}
}

func h264Keyframe(payload []byte) bool {
	nal := payload[0] & 0x1f
	switch nal {
	case 5, 7:
		return true
	case 28:
		if len(payload) < 2 {
			return false
		}
		start := payload[1]&0x80 != 0
		return start && payload[1]&0x1f == 5
	default:
		return false
	}
}
