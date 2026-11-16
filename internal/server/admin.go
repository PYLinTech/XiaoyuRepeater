package server

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"xiaoyurepeater/internal/auth"
	"xiaoyurepeater/internal/store"
	"xiaoyurepeater/internal/version"
)

type keyView struct {
	ID           string     `json:"id"`
	Name         string     `json:"name"`
	Prefix       string     `json:"prefix"`
	CreatedAt    time.Time  `json:"created_at"`
	LastUsedAt   *time.Time `json:"last_used_at"`
	AgentOnline  bool       `json:"agent_online"`
	Rooms        int        `json:"rooms"`
	Participants int        `json:"participants"`
}

type createKeyInput struct {
	Name string `json:"name"`
}

func (s *Server) sessionToken(r *http.Request) string {
	if cookie, err := r.Cookie(auth.SessionCookieName()); err == nil {
		return cookie.Value
	}
	return ""
}

func (s *Server) handleAdminSession(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"authenticated":  s.auth.ValidSession(s.sessionToken(r)),
		"setup_required": s.auth.SetupRequired(),
		"env_managed":    s.auth.EnvManaged(),
		"version":        version.Version,
	})
}

func (s *Server) handleAdminSetup(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		writeError(w, http.StatusForbidden, "来源校验失败")
		return
	}
	if !s.auth.SetupRequired() {
		writeError(w, http.StatusBadRequest, "管理密码已设置")
		return
	}
	var input struct {
		Password string `json:"password"`
	}
	if err := decodeJSON(w, r, &input, maxAdminBody); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.auth.Setup(input.Password); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	token, expires := s.auth.CreateSession()
	s.setSessionCookie(w, r, token, expires)
	s.log.Info("管理密码已初始化", "remote_addr", clientIP(r, s.cfg.TrustProxy))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleAdminLogin(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		writeError(w, http.StatusForbidden, "来源校验失败")
		return
	}
	if s.auth.SetupRequired() {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":          "请先设置管理密码",
			"setup_required": true,
		})
		return
	}
	ip := clientIP(r, s.cfg.TrustProxy)
	if !s.auth.AllowLogin(ip) {
		w.Header().Set("Retry-After", "300")
		writeError(w, http.StatusTooManyRequests, "尝试次数过多，请稍后再试")
		return
	}
	var input struct {
		Password string `json:"password"`
	}
	if err := decodeJSON(w, r, &input, maxAdminBody); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if strings.TrimSpace(input.Password) == "" {
		writeError(w, http.StatusBadRequest, "请输入管理密码")
		return
	}
	if !s.auth.Verify(input.Password) {
		s.auth.RecordLoginFailure(ip)
		s.log.Warn("管理端登录失败", "remote_addr", ip)
		writeError(w, http.StatusUnauthorized, "密码不正确")
		return
	}
	s.auth.ClearLoginFailures(ip)
	token, expires := s.auth.CreateSession()
	s.setSessionCookie(w, r, token, expires)
	s.log.Info("管理端登录成功", "remote_addr", ip)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleAdminLogout(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		writeError(w, http.StatusForbidden, "来源校验失败")
		return
	}
	s.auth.DestroySession(s.sessionToken(r))
	s.clearSessionCookie(w, r)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleAdminPassword(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		writeError(w, http.StatusForbidden, "来源校验失败")
		return
	}
	if s.auth.EnvManaged() {
		writeError(w, http.StatusBadRequest, auth.ErrEnvManaged.Error())
		return
	}
	var input struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if err := decodeJSON(w, r, &input, maxAdminBody); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.auth.ChangePassword(input.CurrentPassword, input.NewPassword); err != nil {
		if errors.Is(err, auth.ErrInvalidLogin) {
			writeError(w, http.StatusBadRequest, "当前密码不正确")
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.auth.DestroyOtherSessions(s.sessionToken(r))
	s.log.Info("管理密码已修改", "remote_addr", clientIP(r, s.cfg.TrustProxy))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleAdminOverview(w http.ResponseWriter, r *http.Request) {
	snapshot := s.hub.Snapshot()
	writeJSON(w, http.StatusOK, map[string]any{
		"version":        version.Version,
		"started_at":     snapshot.StartedAt,
		"uptime_seconds": snapshot.UptimeSeconds,
		"stats":          snapshot.Stats,
		"keys_total":     len(s.store.ListKeys()),
		"rooms":          snapshot.Rooms,
		"agents":         snapshot.Agents,
		"events":         snapshot.Events,
	})
}

func (s *Server) handleAdminKeys(w http.ResponseWriter, r *http.Request) {
	keys := s.store.ListKeys()
	views := make([]keyView, 0, len(keys))
	for _, key := range keys {
		views = append(views, s.keyView(key))
	}
	writeJSON(w, http.StatusOK, map[string]any{"keys": views})
}

func (s *Server) handleAdminCreateKey(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		writeError(w, http.StatusForbidden, "来源校验失败")
		return
	}
	var input createKeyInput
	if err := decodeJSON(w, r, &input, maxAdminBody); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	name := strings.TrimSpace(input.Name)
	if name == "" {
		writeError(w, http.StatusBadRequest, "请填写密钥名称")
		return
	}
	if len([]rune(name)) > 40 {
		writeError(w, http.StatusBadRequest, "密钥名称不能超过 40 个字符")
		return
	}

	s.keyMu.Lock()
	if len(s.store.ListKeys()) >= 200 {
		s.keyMu.Unlock()
		writeError(w, http.StatusBadRequest, "密钥数量已达上限")
		return
	}
	key, secret, err := s.store.CreateKey(name)
	s.keyMu.Unlock()
	if err != nil {
		s.log.Error("创建 API 密钥失败", "error", err)
		writeError(w, http.StatusInternalServerError, "创建密钥失败")
		return
	}
	s.log.Info("已创建 API 密钥", "key_id", key.ID, "name", key.Name)
	writeJSON(w, http.StatusCreated, map[string]any{
		"key":    s.keyView(key),
		"secret": secret,
	})
}

func (s *Server) handleAdminDeleteKey(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		writeError(w, http.StatusForbidden, "来源校验失败")
		return
	}
	id := r.PathValue("id")
	s.keyMu.Lock()
	removed, err := s.store.DeleteKey(id)
	if removed {
		s.hub.CloseRoomsForKey(id, "key_revoked")
	}
	s.keyMu.Unlock()
	if err != nil {
		s.log.Error("删除 API 密钥失败", "error", err, "key_id", id)
		writeError(w, http.StatusInternalServerError, "删除密钥失败")
		return
	}
	if !removed {
		writeError(w, http.StatusNotFound, "密钥不存在")
		return
	}
	s.log.Info("已撤销 API 密钥", "key_id", id)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleAdminRooms(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"rooms": s.hub.ListRooms()})
}

func (s *Server) handleAdminRoom(w http.ResponseWriter, r *http.Request) {
	room, ok := s.hub.GetRoom(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "房间不存在或已关闭")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"room": room})
}

func (s *Server) handleAdminCloseRoom(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		writeError(w, http.StatusForbidden, "来源校验失败")
		return
	}
	id := r.PathValue("id")
	if !s.hub.CloseRoom(id, "closed_by_admin") {
		writeError(w, http.StatusNotFound, "房间不存在或已关闭")
		return
	}
	s.log.Info("管理员关闭房间", "room_id", id)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) keyView(key store.APIKey) keyView {
	online, rooms, participants := s.hub.KeyStatus(key.ID)
	view := keyView{
		ID:           key.ID,
		Name:         key.Name,
		Prefix:       key.Prefix,
		CreatedAt:    key.CreatedAt,
		AgentOnline:  online,
		Rooms:        rooms,
		Participants: participants,
	}
	if !key.LastUsedAt.IsZero() {
		used := key.LastUsedAt
		view.LastUsedAt = &used
	}
	return view
}
