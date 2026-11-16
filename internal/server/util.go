package server

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"

	"xiaoyurepeater/internal/hub"
)

const (
	maxAdminBody    = 64 << 10
	maxConsumerBody = 64 << 10
	maxQueryBytes   = 16 << 10
	maxHeaderBytes  = 16 << 10
)

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{"error": message})
}

func writeHubError(w http.ResponseWriter, err *hub.Error) {
	writeJSON(w, err.Status, map[string]any{"error": err.Message, "code": err.Code})
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any, maxBytes int64) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return errors.New("请求内容过大")
		}
		if errors.Is(err, io.EOF) {
			return errors.New("请求内容为空")
		}
		return errors.New("请求格式不正确")
	}
	if dec.More() {
		return errors.New("请求只能包含一个 JSON 对象")
	}
	return nil
}

func clientIP(r *http.Request, trustProxy bool) string {
	if trustProxy {
		if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
			first := strings.TrimSpace(strings.Split(forwarded, ",")[0])
			if ip := parseIP(first); ip != "" {
				return ip
			}
		}
		if ip := parseIP(strings.TrimSpace(r.Header.Get("X-Real-IP"))); ip != "" {
			return ip
		}
	}
	if ip := parseIP(r.RemoteAddr); ip != "" {
		return ip
	}
	return "unknown"
}

func parseIP(value string) string {
	if host, _, err := net.SplitHostPort(value); err == nil {
		value = host
	}
	value = strings.Trim(value, "[]")
	if net.ParseIP(value) == nil {
		return ""
	}
	return value
}

func (s *Server) isHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	if s.cfg.TrustProxy {
		return strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
	}
	return false
}

func (s *Server) publicWSURL(r *http.Request, path string) string {
	scheme := "ws"
	if s.isHTTPS(r) {
		scheme = "wss"
	}
	return scheme + "://" + r.Host + path
}

// forwardedQuery copies user supplied query values with a hard size budget.
// The values may contain credentials, so they must never be logged.
func forwardedQuery(r *http.Request) map[string][]string {
	out := make(map[string][]string)
	total := 0
	for key, values := range r.URL.Query() {
		if len(out) >= 32 {
			break
		}
		for _, value := range values {
			if len(value) > 2048 {
				value = value[:2048]
			}
			total += len(key) + len(value)
			if total > maxQueryBytes {
				return out
			}
			out[key] = append(out[key], value)
		}
	}
	return out
}

var skippableHeaders = map[string]bool{
	"Connection":               true,
	"Cookie":                   true,
	"Host":                     true,
	"Upgrade":                  true,
	"Content-Length":           true,
	"Transfer-Encoding":        true,
	"Sec-Websocket-Key":        true,
	"Sec-Websocket-Version":    true,
	"Sec-Websocket-Extensions": true,
	"Sec-Websocket-Protocol":   true,
	"Proxy-Connection":         true,
	"Keep-Alive":               true,
	"Te":                       true,
	"Trailer":                  true,
}

// forwardedHeaders passes the headers a user sent during the WebSocket
// handshake to the consumer for authentication. Cookies are always removed so
// control-panel sessions can never leak to a consumer program.
func forwardedHeaders(r *http.Request) map[string][]string {
	out := make(map[string][]string)
	total := 0
	for key, values := range r.Header {
		if skippableHeaders[key] || strings.HasPrefix(key, "Sec-Websocket-") {
			continue
		}
		if len(out) >= 32 {
			break
		}
		for _, value := range values {
			if len(value) > 2048 {
				value = value[:2048]
			}
			total += len(key) + len(value)
			if total > maxHeaderBytes {
				return out
			}
			out[key] = append(out[key], value)
		}
	}
	return out
}

func randomRequestID() string {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "unknown"
	}
	return base64.RawURLEncoding.EncodeToString(buf)
}

func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	parsed, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return strings.EqualFold(parsed.Host, r.Host)
}

func bearerToken(r *http.Request) string {
	value := strings.TrimSpace(r.Header.Get("Authorization"))
	if len(value) > 7 && strings.EqualFold(value[:7], "Bearer ") {
		return strings.TrimSpace(value[7:])
	}
	return ""
}
