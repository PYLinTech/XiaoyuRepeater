package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"xiaoyurepeater/internal/hub"
	"xiaoyurepeater/internal/protocol"
	"xiaoyurepeater/internal/store"
)

func (s *Server) consumerKey(r *http.Request) (store.APIKey, bool) {
	secret := bearerToken(r)
	if secret == "" {
		secret = strings.TrimSpace(r.Header.Get("X-Api-Key"))
	}
	if secret == "" {
		return store.APIKey{}, false
	}
	key, ok := s.store.LookupKey(secret)
	return key, ok
}

func (s *Server) requireConsumer(w http.ResponseWriter, r *http.Request) (store.APIKey, bool) {
	secret := bearerToken(r)
	if secret == "" {
		secret = strings.TrimSpace(r.Header.Get("X-Api-Key"))
	}
	if secret == "" {
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeErrorCode(w, http.StatusUnauthorized, "missing_api_key", "缺少 API Key")
		return store.APIKey{}, false
	}
	key, ok := s.store.LookupKey(secret)
	if !ok {
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeErrorCode(w, http.StatusUnauthorized, "invalid_api_key", "API Key 无效")
		return store.APIKey{}, false
	}
	return key, true
}

func (s *Server) handleConsumerCreateRoom(w http.ResponseWriter, r *http.Request) {
	key, ok := s.requireConsumer(w, r)
	if !ok {
		return
	}
	var input struct {
		Name            string          `json:"name"`
		Metadata        json.RawMessage `json:"metadata"`
		MaxParticipants int             `json:"max_participants"`
	}
	if err := decodeJSON(w, r, &input, maxConsumerBody); err != nil {
		writeErrorCode(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	s.keyMu.Lock()
	if _, stillExists := s.store.GetKey(key.ID); !stillExists {
		s.keyMu.Unlock()
		writeErrorCode(w, http.StatusUnauthorized, "api_key_revoked", "API Key 已被撤销")
		return
	}
	room, hubErr := s.hub.CreateRoom(key.ID, key.Name, hub.RoomOptions{
		Name:            input.Name,
		Metadata:        input.Metadata,
		MaxParticipants: input.MaxParticipants,
	})
	s.keyMu.Unlock()
	if hubErr != nil {
		writeHubError(w, hubErr)
		return
	}
	if err := s.store.TouchKey(key.ID, time.Now()); err != nil {
		s.log.Warn("更新密钥使用时间失败", "error", err, "key_id", key.ID)
	}

	wsPath := "/v1/rooms/" + room.ID + "/ws"
	rtcPath := "/v1/rooms/" + room.ID + "/webrtc"
	response := map[string]any{
		"protocol": protocol.Version,
		"room":     room,
		"ws_path":  wsPath,
		"ws_url":   s.publicWSURL(r, wsPath),
	}
	if s.cfg.WebRTCEnabled {
		response["webrtc_path"] = rtcPath
		response["webrtc_url"] = s.publicWSURL(r, rtcPath)
	}
	s.log.Info("使用方创建房间", "room_id", room.ID, "key_id", key.ID, "name", room.Name)
	writeJSON(w, http.StatusCreated, response)
}

func (s *Server) handleConsumerListRooms(w http.ResponseWriter, r *http.Request) {
	key, ok := s.requireConsumer(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"rooms": s.hub.RoomsForKey(key.ID)})
}

func (s *Server) handleConsumerGetRoom(w http.ResponseWriter, r *http.Request) {
	key, ok := s.requireConsumer(w, r)
	if !ok {
		return
	}
	room, found := s.hub.GetRoom(r.PathValue("id"))
	if !found || room.KeyID != key.ID {
		writeErrorCode(w, http.StatusNotFound, "room_not_found", "房间不存在或已关闭")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"room": room})
}

func (s *Server) handleConsumerCloseRoom(w http.ResponseWriter, r *http.Request) {
	key, ok := s.requireConsumer(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	s.keyMu.Lock()
	if _, stillExists := s.store.GetKey(key.ID); !stillExists {
		s.keyMu.Unlock()
		writeErrorCode(w, http.StatusUnauthorized, "api_key_revoked", "API Key 已被撤销")
		return
	}
	room, found := s.hub.GetRoom(id)
	if !found || room.KeyID != key.ID {
		s.keyMu.Unlock()
		writeErrorCode(w, http.StatusNotFound, "room_not_found", "房间不存在或已关闭")
		return
	}
	closed := s.hub.CloseRoom(id, "closed_by_consumer")
	s.keyMu.Unlock()
	if !closed {
		writeErrorCode(w, http.StatusNotFound, "room_not_found", "房间不存在或已关闭")
		return
	}
	s.log.Info("使用方关闭房间", "room_id", id, "key_id", key.ID)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleAgentWS(w http.ResponseWriter, r *http.Request) {
	key, ok := s.requireConsumer(w, r)
	if !ok {
		return
	}
	if err := s.store.TouchKey(key.ID, time.Now()); err != nil {
		s.log.Warn("更新密钥使用时间失败", "error", err, "key_id", key.ID)
	}

	upgrader := websocket.Upgrader{
		HandshakeTimeout: 10 * time.Second,
		ReadBufferSize:   4096,
		WriteBufferSize:  4096,
		CheckOrigin:      func(*http.Request) bool { return true },
	}
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		s.log.Warn("使用方 WebSocket 升级失败", "error", err, "key_id", key.ID)
		return
	}
	s.hub.ServeAgent(ws, key.ID, key.Name, clientIP(r, s.cfg.TrustProxy))
}

func (s *Server) handleUserWS(w http.ResponseWriter, r *http.Request) {
	roomID := r.PathValue("id")
	remoteAddr := clientIP(r, s.cfg.TrustProxy)
	query := forwardedQuery(r)
	headers := forwardedHeaders(r)

	room, decision, hubErr := s.hub.AuthorizeJoin(
		r.Context(), roomID, protocol.TransportWebSocket, remoteAddr, query, headers)
	if hubErr != nil {
		s.log.Info("用户加入房间被拒绝",
			"room_id", roomID,
			"transport", protocol.TransportWebSocket,
			"code", hubErr.Code,
			"remote_addr", remoteAddr,
			"request_id", w.Header().Get("X-Request-Id"))
		writeHubError(w, hubErr)
		return
	}
	if serveErr := s.hub.ServeParticipant(w, r, room, decision, remoteAddr); serveErr != nil {
		writeHubError(w, serveErr)
	}
}

func (s *Server) handleUserWebRTC(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.WebRTCEnabled {
		writeHubError(w, hub.ErrWebRTCUnavailable)
		return
	}
	roomID := r.PathValue("id")
	remoteAddr := clientIP(r, s.cfg.TrustProxy)
	query := forwardedQuery(r)
	headers := forwardedHeaders(r)

	room, decision, hubErr := s.hub.AuthorizeJoin(
		r.Context(), roomID, protocol.TransportWebRTC, remoteAddr, query, headers)
	if hubErr != nil {
		s.log.Info("用户加入房间被拒绝",
			"room_id", roomID,
			"transport", protocol.TransportWebRTC,
			"code", hubErr.Code,
			"remote_addr", remoteAddr,
			"request_id", w.Header().Get("X-Request-Id"))
		writeHubError(w, hubErr)
		return
	}
	if serveErr := s.hub.ServeRTCPeer(w, r, room, decision, remoteAddr); serveErr != nil {
		writeHubError(w, serveErr)
	}
}
