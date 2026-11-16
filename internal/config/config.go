package config

import (
	"fmt"
	"log/slog"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is loaded from environment variables so the binary stays
// single-file and container friendly.
type Config struct {
	Addr                   string
	DataDir                string
	AdminPassword          string
	TLSCert                string
	TLSKey                 string
	TrustProxy             bool
	AuthTimeout            time.Duration
	SessionTTL             time.Duration
	MaxRoomsPerKey         int
	DefaultMaxParticipants int
	MaxParticipantsCap     int
	MaxFrameBytes          int64
	WebRTCEnabled          bool
	WebRTCUDPPortMin       int
	WebRTCUDPPortMax       int
	WebRTCPublicIPs        []string
	WebRTCMaxTracksPerPeer int
	LogLevel               slog.Level
}

func Load() (Config, error) {
	cfg := Config{
		Addr:                   env("XR_ADDR", ":8080"),
		DataDir:                env("XR_DATA_DIR", "./data"),
		AdminPassword:          os.Getenv("XR_ADMIN_PASSWORD"),
		TLSCert:                os.Getenv("XR_TLS_CERT"),
		TLSKey:                 os.Getenv("XR_TLS_KEY"),
		TrustProxy:             envBool("XR_TRUST_PROXY", false),
		AuthTimeout:            envDuration("XR_AUTH_TIMEOUT", 10*time.Second),
		SessionTTL:             envDuration("XR_SESSION_TTL", 12*time.Hour),
		MaxRoomsPerKey:         envInt("XR_MAX_ROOMS_PER_KEY", 100),
		DefaultMaxParticipants: envInt("XR_DEFAULT_MAX_PARTICIPANTS", 50),
		MaxParticipantsCap:     envInt("XR_MAX_PARTICIPANTS", 500),
		MaxFrameBytes:          envInt64("XR_MAX_FRAME_BYTES", 1<<20),
		WebRTCEnabled:          envBool("XR_WEBRTC_ENABLED", true),
		WebRTCUDPPortMin:       envInt("XR_WEBRTC_UDP_PORT_MIN", 50000),
		WebRTCUDPPortMax:       envInt("XR_WEBRTC_UDP_PORT_MAX", 50100),
		WebRTCPublicIPs:        envList("XR_WEBRTC_PUBLIC_IP"),
		WebRTCMaxTracksPerPeer: envInt("XR_WEBRTC_MAX_TRACKS_PER_PEER", 8),
		LogLevel:               slog.LevelInfo,
	}

	switch strings.ToLower(env("XR_LOG_LEVEL", "info")) {
	case "debug":
		cfg.LogLevel = slog.LevelDebug
	case "warn", "warning":
		cfg.LogLevel = slog.LevelWarn
	case "error":
		cfg.LogLevel = slog.LevelError
	case "info":
	default:
		return cfg, fmt.Errorf("XR_LOG_LEVEL 无效")
	}

	if cfg.AdminPassword != "" && len(cfg.AdminPassword) < 8 {
		return cfg, fmt.Errorf("XR_ADMIN_PASSWORD 至少需要 8 个字符")
	}
	if (cfg.TLSCert == "") != (cfg.TLSKey == "") {
		return cfg, fmt.Errorf("XR_TLS_CERT 与 XR_TLS_KEY 必须同时配置")
	}
	if cfg.AuthTimeout < time.Second || cfg.AuthTimeout > 2*time.Minute {
		return cfg, fmt.Errorf("XR_AUTH_TIMEOUT 需要在 1s 到 2m 之间")
	}
	if cfg.SessionTTL < time.Minute || cfg.SessionTTL > 30*24*time.Hour {
		return cfg, fmt.Errorf("XR_SESSION_TTL 需要在 1m 到 720h 之间")
	}
	if cfg.MaxRoomsPerKey < 1 || cfg.MaxRoomsPerKey > 10000 {
		return cfg, fmt.Errorf("XR_MAX_ROOMS_PER_KEY 需要在 1 到 10000 之间")
	}
	if cfg.DefaultMaxParticipants < 2 || cfg.DefaultMaxParticipants > 10000 {
		return cfg, fmt.Errorf("XR_DEFAULT_MAX_PARTICIPANTS 需要在 2 到 10000 之间")
	}
	if cfg.MaxParticipantsCap < cfg.DefaultMaxParticipants || cfg.MaxParticipantsCap > 10000 {
		return cfg, fmt.Errorf("XR_MAX_PARTICIPANTS 不能小于默认值，且不超过 10000")
	}
	if cfg.MaxFrameBytes < 1024 || cfg.MaxFrameBytes > 64<<20 {
		return cfg, fmt.Errorf("XR_MAX_FRAME_BYTES 需要在 1KB 到 64MB 之间")
	}
	if cfg.WebRTCUDPPortMin < 1024 || cfg.WebRTCUDPPortMin > 65535 ||
		cfg.WebRTCUDPPortMax < 1024 || cfg.WebRTCUDPPortMax > 65535 {
		return cfg, fmt.Errorf("XR_WEBRTC_UDP_PORT_MIN / XR_WEBRTC_UDP_PORT_MAX 需要在 1024 到 65535 之间")
	}
	if cfg.WebRTCUDPPortMin > cfg.WebRTCUDPPortMax {
		return cfg, fmt.Errorf("XR_WEBRTC_UDP_PORT_MIN 不能大于 XR_WEBRTC_UDP_PORT_MAX")
	}
	for _, ip := range cfg.WebRTCPublicIPs {
		if net.ParseIP(ip) == nil {
			return cfg, fmt.Errorf("XR_WEBRTC_PUBLIC_IP 含有无效 IP：%s", ip)
		}
	}
	if cfg.WebRTCMaxTracksPerPeer < 1 || cfg.WebRTCMaxTracksPerPeer > 32 {
		return cfg, fmt.Errorf("XR_WEBRTC_MAX_TRACKS_PER_PEER 需要在 1 到 32 之间")
	}
	return cfg, nil
}

func env(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil {
		return fallback
	}
	return n
}

func envInt64(key string, fallback int64) int64 {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	if err != nil {
		return fallback
	}
	return n
}

func envList(key string) []string {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return nil
	}
	var out []string
	for _, item := range strings.Split(raw, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}

func envBool(key string, fallback bool) bool {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return fallback
	}
	return b
}

func envDuration(key string, fallback time.Duration) time.Duration {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return fallback
	}
	return d
}
