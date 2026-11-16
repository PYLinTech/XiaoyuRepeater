package server

import (
	"bufio"
	"errors"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"path"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"xiaoyurepeater/internal/auth"
	"xiaoyurepeater/internal/config"
	"xiaoyurepeater/internal/hub"
	"xiaoyurepeater/internal/store"
	"xiaoyurepeater/internal/version"
	webassets "xiaoyurepeater/web"
)

type Server struct {
	cfg   config.Config
	log   *slog.Logger
	store *store.Store
	auth  *auth.Service
	hub   *hub.Hub
	mux   *http.ServeMux
	dist  fs.FS

	keyMu sync.Mutex
}

func New(cfg config.Config, st *store.Store, logger *slog.Logger) (*Server, error) {
	authSvc, err := auth.NewService(st, cfg.AdminPassword, cfg.SessionTTL)
	if err != nil {
		return nil, err
	}
	dist, err := fs.Sub(webassets.FS, "dist")
	if err != nil {
		return nil, err
	}
	s := &Server{
		cfg:   cfg,
		log:   logger,
		store: st,
		auth:  authSvc,
		mux:   http.NewServeMux(),
		dist:  dist,
	}
	engine, err := hub.New(hub.Config{
		MaxRoomsPerKey:         cfg.MaxRoomsPerKey,
		DefaultMaxParticipants: cfg.DefaultMaxParticipants,
		MaxParticipantsCap:     cfg.MaxParticipantsCap,
		MaxFrameBytes:          cfg.MaxFrameBytes,
		AuthTimeout:            cfg.AuthTimeout,
		WebRTC: hub.WebRTCConfig{
			Enabled:          cfg.WebRTCEnabled,
			UDPPortMin:       cfg.WebRTCUDPPortMin,
			UDPPortMax:       cfg.WebRTCUDPPortMax,
			PublicIPs:        cfg.WebRTCPublicIPs,
			MaxTracksPerPeer: cfg.WebRTCMaxTracksPerPeer,
		},
	}, logger)
	if err != nil {
		return nil, err
	}
	s.hub = engine
	s.routes()
	return s, nil
}

func (s *Server) Close() {
	s.hub.Shutdown()
}

func (s *Server) AdminSetupRequired() bool {
	return s.auth.SetupRequired()
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /healthz", s.handleHealth)

	s.mux.HandleFunc("GET /api/admin/session", s.handleAdminSession)
	s.mux.HandleFunc("POST /api/admin/setup", s.handleAdminSetup)
	s.mux.HandleFunc("POST /api/admin/login", s.handleAdminLogin)
	s.mux.HandleFunc("POST /api/admin/logout", s.handleAdminLogout)
	s.mux.HandleFunc("PUT /api/admin/password", s.requireAdmin(s.handleAdminPassword))
	s.mux.HandleFunc("GET /api/admin/overview", s.requireAdmin(s.handleAdminOverview))
	s.mux.HandleFunc("GET /api/admin/rooms", s.requireAdmin(s.handleAdminRooms))
	s.mux.HandleFunc("GET /api/admin/rooms/{id}", s.requireAdmin(s.handleAdminRoom))
	s.mux.HandleFunc("DELETE /api/admin/rooms/{id}", s.requireAdmin(s.handleAdminCloseRoom))
	s.mux.HandleFunc("GET /api/admin/keys", s.requireAdmin(s.handleAdminKeys))
	s.mux.HandleFunc("POST /api/admin/keys", s.requireAdmin(s.handleAdminCreateKey))
	s.mux.HandleFunc("DELETE /api/admin/keys/{id}", s.requireAdmin(s.handleAdminDeleteKey))
	s.mux.HandleFunc("GET /api/sdk/version", s.handleSDKVersion)
	s.mux.HandleFunc("GET /api/sdk/download", s.handleSDKDownload)

	s.mux.HandleFunc("POST /v1/rooms", s.handleConsumerCreateRoom)
	s.mux.HandleFunc("GET /v1/rooms", s.handleConsumerListRooms)
	s.mux.HandleFunc("DELETE /v1/rooms/{id}", s.handleConsumerCloseRoom)
	s.mux.HandleFunc("GET /v1/rooms/{id}/ws", s.handleUserWS)
	s.mux.HandleFunc("GET /v1/rooms/{id}/webrtc", s.handleUserWebRTC)
	s.mux.HandleFunc("GET /v1/agent", s.handleAgentWS)

	s.mux.HandleFunc("/api/", s.handleAPINotFound)
	s.mux.HandleFunc("/v1/", s.handleAPINotFound)
	s.mux.HandleFunc("/", s.handleSPA)
}

func (s *Server) Handler() http.Handler {
	var handler http.Handler = s.mux
	handler = s.logging(handler)
	handler = s.requestID(handler)
	handler = s.securityHeaders(handler)
	handler = s.recoverer(handler)
	return handler
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	snapshot := s.hub.Snapshot()
	writeJSON(w, http.StatusOK, map[string]any{
		"status":         "ok",
		"version":        version.Version,
		"uptime_seconds": snapshot.UptimeSeconds,
	})
}

func (s *Server) handleAPINotFound(w http.ResponseWriter, r *http.Request) {
	writeError(w, http.StatusNotFound, "接口不存在")
}

func (s *Server) handleSPA(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		writeError(w, http.StatusMethodNotAllowed, "方法不支持")
		return
	}
	rel := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
	if rel == "" || rel == "." || !fs.ValidPath(rel) {
		rel = "index.html"
	}
	if info, err := fs.Stat(s.dist, rel); err != nil || info.IsDir() {
		rel = "index.html"
	}
	if strings.HasPrefix(rel, "assets/") {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "no-cache")
	}
	http.ServeFileFS(w, r, s.dist, rel)
}

func (s *Server) requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := ""
		if cookie, err := r.Cookie(auth.SessionCookieName()); err == nil {
			token = cookie.Value
		}
		if !s.auth.ValidSession(token) {
			writeError(w, http.StatusUnauthorized, "登录已过期，请重新登录")
			return
		}
		next(w, r)
	}
}

func (s *Server) setSessionCookie(w http.ResponseWriter, r *http.Request, token string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     auth.SessionCookieName(),
		Value:    token,
		Path:     "/",
		Expires:  expires,
		MaxAge:   int(time.Until(expires).Seconds()),
		HttpOnly: true,
		Secure:   s.isHTTPS(r),
		SameSite: http.SameSiteStrictMode,
	})
}

func (s *Server) clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     auth.SessionCookieName(),
		Value:    "",
		Path:     "/",
		Expires:  time.Unix(1, 0),
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   s.isHTTPS(r),
		SameSite: http.SameSiteStrictMode,
	})
}

func (s *Server) requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := randomRequestID()
		w.Header().Set("X-Request-Id", id)
		next.ServeHTTP(w, r)
	})
}

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		h.Set("Content-Security-Policy",
			"default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; script-src 'self'; connect-src 'self'; base-uri 'none'; frame-ancestors 'none'")
		if s.isHTTPS(r) {
			h.Set("Strict-Transport-Security", "max-age=31536000")
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				s.log.Error("请求处理发生 panic",
					"error", recovered,
					"method", r.Method,
					"path", r.URL.Path,
					"stack", string(debug.Stack()))
				writeError(w, http.StatusInternalServerError, "服务器内部错误")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func (s *Server) logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		recorder := &loggingWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(recorder, r)
		elapsed := time.Since(started)

		level := slog.LevelDebug
		if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/v1/") || r.URL.Path == "/healthz" {
			level = slog.LevelInfo
		}
		s.log.Log(r.Context(), level, "http",
			"method", r.Method,
			"path", r.URL.Path,
			"status", recorder.status,
			"duration", elapsed.String(),
			"remote_addr", clientIP(r, s.cfg.TrustProxy),
			"request_id", recorder.Header().Get("X-Request-Id"),
		)
	})
}

type loggingWriter struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (w *loggingWriter) WriteHeader(status int) {
	if !w.wrote {
		w.status = status
		w.wrote = true
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *loggingWriter) Write(data []byte) (int, error) {
	if !w.wrote {
		w.status = http.StatusOK
		w.wrote = true
	}
	return w.ResponseWriter.Write(data)
}

func (w *loggingWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("当前 ResponseWriter 不支持连接接管")
	}
	w.status = http.StatusSwitchingProtocols
	w.wrote = true
	return hijacker.Hijack()
}

func (w *loggingWriter) Flush() {
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (w *loggingWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}
