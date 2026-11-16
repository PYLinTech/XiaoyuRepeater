package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"xiaoyurepeater/internal/config"
	"xiaoyurepeater/internal/hub"
	"xiaoyurepeater/internal/protocol"
	"xiaoyurepeater/internal/store"
)

type testEnv struct {
	t      *testing.T
	ts     *httptest.Server
	srv    *Server
	st     *store.Store
	client *http.Client
	secret string
	keyID  string
}

func newTestEnv(t *testing.T, tweak func(*config.Config)) *testEnv {
	t.Helper()
	cfg := config.Config{
		DataDir:                filepath.Join(t.TempDir(), "data"),
		AuthTimeout:            2 * time.Second,
		SessionTTL:             time.Hour,
		MaxRoomsPerKey:         20,
		DefaultMaxParticipants: 10,
		MaxParticipantsCap:     50,
		MaxFrameBytes:          1 << 20,
		LogLevel:               slog.LevelError,
	}
	if tweak != nil {
		tweak(&cfg)
	}
	st, err := store.Open(cfg.DataDir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	srv, err := New(cfg, st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	t.Cleanup(srv.Close)

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookie jar: %v", err)
	}
	client := &http.Client{Jar: jar, Timeout: 5 * time.Second}
	env := &testEnv{t: t, ts: ts, srv: srv, st: st, client: client}

	status, body := env.adminJSON(http.MethodPost, "/api/admin/setup", map[string]any{"password": "test-password"}, nil)
	if status != http.StatusOK {
		t.Fatalf("setup: status=%d body=%s", status, body)
	}
	var created struct {
		Key    keyView `json:"key"`
		Secret string  `json:"secret"`
	}
	status, body = env.adminJSON(http.MethodPost, "/api/admin/keys", map[string]any{"name": "测试使用方"}, &created)
	if status != http.StatusCreated {
		t.Fatalf("create key: status=%d body=%s", status, body)
	}
	if !strings.HasPrefix(created.Secret, "xr_") {
		t.Fatalf("unexpected secret: %q", created.Secret)
	}
	env.secret = created.Secret
	env.keyID = created.Key.ID
	return env
}

func (e *testEnv) adminJSON(method, path string, body any, out any) (int, []byte) {
	e.t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			e.t.Fatalf("marshal: %v", err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, e.ts.URL+path, reader)
	if err != nil {
		e.t.Fatalf("request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := e.client.Do(req)
	if err != nil {
		e.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			e.t.Fatalf("decode admin response %s: %v (%s)", path, err, raw)
		}
	}
	return resp.StatusCode, raw
}

func (e *testEnv) consumerJSON(method, path string, body any, out any) (int, []byte) {
	e.t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			e.t.Fatalf("marshal: %v", err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, e.ts.URL+path, reader)
	if err != nil {
		e.t.Fatalf("request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+e.secret)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			e.t.Fatalf("decode consumer response %s: %v (%s)", path, err, raw)
		}
	}
	return resp.StatusCode, raw
}

func (e *testEnv) createRoom(name string, maxParticipants int) hub.RoomInfo {
	e.t.Helper()
	var created struct {
		Room     hub.RoomInfo `json:"room"`
		WSPath   string       `json:"ws_path"`
		WSURL    string       `json:"ws_url"`
		Protocol int          `json:"protocol"`
	}
	status, body := e.consumerJSON(http.MethodPost, "/v1/rooms", map[string]any{
		"name":             name,
		"max_participants": maxParticipants,
	}, &created)
	if status != http.StatusCreated {
		e.t.Fatalf("create room: status=%d body=%s", status, body)
	}
	if created.Protocol != protocol.Version {
		e.t.Fatalf("protocol = %d", created.Protocol)
	}
	if !strings.HasPrefix(created.WSPath, "/v1/rooms/") {
		e.t.Fatalf("ws_path = %q", created.WSPath)
	}
	return created.Room
}

func wsBaseURL(httpURL string) string {
	return strings.Replace(httpURL, "http://", "ws://", 1)
}

type agentHarness struct {
	ws       *websocket.Conn
	events   chan map[string]any
	closeErr chan error
	done     chan struct{}
	policy   func(token string) *protocol.AuthResponse
}

func newAgentHarness(t *testing.T, tsURL, secret string, policy func(token string) *protocol.AuthResponse) (*agentHarness, protocol.Hello) {
	t.Helper()
	header := http.Header{}
	header.Set("X-Api-Key", secret)
	ws, resp, err := websocket.DefaultDialer.Dial(wsBaseURL(tsURL)+"/v1/agent", header)
	if err != nil {
		status := 0
		if resp != nil {
			status = resp.StatusCode
		}
		t.Fatalf("dial agent: %v (status=%d)", err, status)
	}
	_ = ws.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, raw, err := ws.ReadMessage()
	if err != nil {
		t.Fatalf("read hello: %v", err)
	}
	var hello protocol.Hello
	if err := json.Unmarshal(raw, &hello); err != nil {
		t.Fatalf("decode hello: %v (%s)", err, raw)
	}
	if hello.Type != protocol.TypeHello {
		t.Fatalf("hello type = %q", hello.Type)
	}
	h := &agentHarness{
		ws:       ws,
		events:   make(chan map[string]any, 256),
		closeErr: make(chan error, 1),
		done:     make(chan struct{}),
		policy:   policy,
	}
	go h.readLoop()
	t.Cleanup(func() {
		_ = ws.Close()
		select {
		case <-h.done:
		case <-time.After(2 * time.Second):
			t.Log("agent harness did not stop in time")
		}
	})
	return h, hello
}

func (h *agentHarness) readLoop() {
	defer close(h.done)
	for {
		_, raw, err := h.ws.ReadMessage()
		if err != nil {
			select {
			case h.closeErr <- err:
			default:
			}
			return
		}
		var envelope map[string]any
		if err := json.Unmarshal(raw, &envelope); err != nil {
			continue
		}
		kind, _ := envelope["type"].(string)
		if kind == protocol.TypeAuthRequest {
			var req protocol.AuthRequest
			if err := json.Unmarshal(raw, &req); err != nil {
				continue
			}
			token := ""
			if values := req.Query["token"]; len(values) > 0 {
				token = values[0]
			}
			if h.policy != nil {
				if resp := h.policy(token); resp != nil {
					resp.Type = protocol.TypeAuthResponse
					resp.RequestID = req.RequestID
					payload, _ := json.Marshal(resp)
					_ = h.ws.SetWriteDeadline(time.Now().Add(2 * time.Second))
					if err := h.ws.WriteMessage(websocket.TextMessage, payload); err != nil {
						return
					}
				}
			}
		}
		select {
		case h.events <- envelope:
		default:
		}
	}
}

func (h *agentHarness) waitClose(t *testing.T, timeout time.Duration) error {
	t.Helper()
	select {
	case err := <-h.closeErr:
		return err
	case <-time.After(timeout):
		t.Fatalf("timed out waiting for agent close")
		return nil
	}
}

func (h *agentHarness) waitEvent(t *testing.T, kind string, timeout time.Duration) map[string]any {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case event := <-h.events:
			if event["type"] == kind {
				return event
			}
		case <-deadline:
			t.Fatalf("timed out waiting for agent event %q", kind)
		}
	}
}

type userConn struct {
	ws      *websocket.Conn
	headers http.Header
}

func dialUser(t *testing.T, tsURL, roomID, token string) (*userConn, int, []byte) {
	t.Helper()
	target := fmt.Sprintf("%s/v1/rooms/%s/ws?token=%s", wsBaseURL(tsURL), roomID, url.QueryEscape(token))
	ws, resp, err := websocket.DefaultDialer.Dial(target, nil)
	if err != nil {
		status := 0
		var body []byte
		if resp != nil {
			status = resp.StatusCode
			body, _ = io.ReadAll(io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
		}
		return nil, status, body
	}
	_ = ws.SetReadDeadline(time.Now().Add(3 * time.Second))
	return &userConn{ws: ws, headers: resp.Header}, resp.StatusCode, nil
}

func mustDialUser(t *testing.T, tsURL, roomID, token string) *userConn {
	t.Helper()
	user, status, body := dialUser(t, tsURL, roomID, token)
	if user == nil {
		t.Fatalf("dial user %q: status=%d body=%s", token, status, body)
	}
	t.Cleanup(func() { _ = user.ws.Close() })
	return user
}

func readMessage(t *testing.T, ws *websocket.Conn) (int, []byte) {
	t.Helper()
	_ = ws.SetReadDeadline(time.Now().Add(3 * time.Second))
	kind, data, err := ws.ReadMessage()
	if err != nil {
		t.Fatalf("read ws message: %v", err)
	}
	return kind, data
}

func allowTokens(prefix string) func(string) *protocol.AuthResponse {
	return func(token string) *protocol.AuthResponse {
		if strings.HasPrefix(token, prefix) {
			allow := true
			return &protocol.AuthResponse{Allow: &allow, Subject: token}
		}
		allow := false
		return &protocol.AuthResponse{Allow: &allow, Reason: "令牌无效"}
	}
}

func TestEndToEndRelayAuthAndClose(t *testing.T) {
	env := newTestEnv(t, nil)
	agent, hello := newAgentHarness(t, env.ts.URL, env.secret, allowTokens("ok-"))
	if hello.KeyID != env.keyID || hello.Protocol != protocol.Version {
		t.Fatalf("unexpected hello: %+v", hello)
	}

	room := env.createRoom("产品评审", 10)
	alice := mustDialUser(t, env.ts.URL, room.ID, "ok-alice")
	bob := mustDialUser(t, env.ts.URL, room.ID, "ok-bob")

	subjectHeader, _ := url.QueryUnescape(alice.headers.Get("X-Repeater-Subject"))
	if subjectHeader != "ok-alice" {
		t.Fatalf("subject header = %q", subjectHeader)
	}
	if alice.headers.Get("X-Repeater-Room") != room.ID {
		t.Fatalf("room header = %q", alice.headers.Get("X-Repeater-Room"))
	}

	agent.waitEvent(t, protocol.TypeUserJoined, 2*time.Second)
	agent.waitEvent(t, protocol.TypeUserJoined, 2*time.Second)

	if err := alice.ws.WriteMessage(websocket.TextMessage, []byte("大家好")); err != nil {
		t.Fatalf("alice write: %v", err)
	}
	kind, data := readMessage(t, bob.ws)
	if kind != websocket.TextMessage || string(data) != "大家好" {
		t.Fatalf("relay text = %d %q", kind, data)
	}

	binary := []byte{0, 1, 2, 255}
	if err := bob.ws.WriteMessage(websocket.BinaryMessage, binary); err != nil {
		t.Fatalf("bob write: %v", err)
	}
	kind, data = readMessage(t, alice.ws)
	if kind != websocket.BinaryMessage || !bytes.Equal(data, binary) {
		t.Fatalf("relay binary = %d %v", kind, data)
	}

	var overview struct {
		Stats     hub.StatsSnapshot `json:"stats"`
		KeysTotal int               `json:"keys_total"`
		Rooms     []hub.RoomInfo    `json:"rooms"`
	}
	status, body := env.adminJSON(http.MethodGet, "/api/admin/overview", nil, &overview)
	if status != http.StatusOK {
		t.Fatalf("overview status=%d body=%s", status, body)
	}
	if overview.Stats.Participants != 2 || overview.Stats.AgentsOnline != 1 || overview.KeysTotal != 1 {
		t.Fatalf("unexpected overview: %+v", overview.Stats)
	}
	if len(overview.Rooms) != 1 || overview.Rooms[0].Messages != 2 {
		t.Fatalf("unexpected room snapshot: %+v", overview.Rooms)
	}

	if _, status, body = dialUser(t, env.ts.URL, room.ID, "deny-me"); status != http.StatusForbidden {
		t.Fatalf("denied join status=%d body=%s", status, body)
	} else if !strings.Contains(string(body), "鉴权未通过") {
		t.Fatalf("denied body=%s", body)
	}

	if _, status, body = dialUser(t, env.ts.URL, "r_missing", "ok-x"); status != http.StatusNotFound {
		t.Fatalf("missing room status=%d body=%s", status, body)
	}

	status, body = env.adminJSON(http.MethodDelete, "/api/admin/rooms/"+room.ID, nil, nil)
	if status != http.StatusOK {
		t.Fatalf("close room status=%d body=%s", status, body)
	}
	agent.waitEvent(t, protocol.TypeRoomClosed, 2*time.Second)

	for _, user := range []*userConn{alice, bob} {
		_ = user.ws.SetReadDeadline(time.Now().Add(2 * time.Second))
		_, _, err := user.ws.ReadMessage()
		if !websocket.IsCloseError(err, 4000) {
			t.Fatalf("expected close code 4000, got %v", err)
		}
	}

	if _, status, _ = dialUser(t, env.ts.URL, room.ID, "ok-late"); status != http.StatusNotFound {
		t.Fatalf("closed room join status=%d", status)
	}
}

func TestAgentOfflineReconnectAndKeyRevoke(t *testing.T) {
	env := newTestEnv(t, nil)
	room := env.createRoom("远程协助", 10)

	if _, status, body := dialUser(t, env.ts.URL, room.ID, "ok-a"); status != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 without agent, got %d body=%s", status, body)
	}

	agent, hello := newAgentHarness(t, env.ts.URL, env.secret, allowTokens("ok-"))
	if len(hello.Rooms) != 1 || hello.Rooms[0].ID != room.ID {
		t.Fatalf("hello rooms = %+v", hello.Rooms)
	}
	user := mustDialUser(t, env.ts.URL, room.ID, "ok-a")
	_ = user.ws.Close()
	agent.waitEvent(t, protocol.TypeUserLeft, 2*time.Second)

	_ = agent.ws.Close()
	deadline := time.Now().Add(3 * time.Second)
	for {
		var overview struct {
			Stats hub.StatsSnapshot `json:"stats"`
		}
		env.adminJSON(http.MethodGet, "/api/admin/overview", nil, &overview)
		if overview.Stats.AgentsOnline == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("agent did not disconnect")
		}
		time.Sleep(20 * time.Millisecond)
	}

	if _, status, body := dialUser(t, env.ts.URL, room.ID, "ok-a"); status != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 after agent disconnect, got %d body=%s", status, body)
	}

	agent2, hello2 := newAgentHarness(t, env.ts.URL, env.secret, allowTokens("ok-"))
	if len(hello2.Rooms) != 1 || hello2.Rooms[0].ID != room.ID {
		t.Fatalf("reconnect hello rooms = %+v", hello2.Rooms)
	}
	mustDialUser(t, env.ts.URL, room.ID, "ok-b")

	status, body := env.adminJSON(http.MethodDelete, "/api/admin/keys/"+env.keyID, nil, nil)
	if status != http.StatusOK {
		t.Fatalf("revoke key status=%d body=%s", status, body)
	}
	if err := agent2.waitClose(t, 2*time.Second); !websocket.IsCloseError(err, 4003) {
		t.Fatalf("expected agent close 4003, got %v", err)
	}

	status, body = env.consumerJSON(http.MethodPost, "/v1/rooms", map[string]any{"name": "x"}, nil)
	if status != http.StatusUnauthorized {
		t.Fatalf("revoked key create room status=%d body=%s", status, body)
	}
	var rooms struct {
		Rooms []hub.RoomInfo `json:"rooms"`
	}
	env.adminJSON(http.MethodGet, "/api/admin/rooms", nil, &rooms)
	if len(rooms.Rooms) != 0 {
		t.Fatalf("rooms after revoke = %+v", rooms.Rooms)
	}
}

func TestAuthTimeoutAndRoomCapacity(t *testing.T) {
	env := newTestEnv(t, func(cfg *config.Config) { cfg.AuthTimeout = 300 * time.Millisecond })
	policy := func(token string) *protocol.AuthResponse {
		if token == "slow" {
			return nil
		}
		allow := true
		return &protocol.AuthResponse{Allow: &allow, Subject: token}
	}
	newAgentHarness(t, env.ts.URL, env.secret, policy)
	room := env.createRoom("容量测试", 2)

	if _, status, body := dialUser(t, env.ts.URL, room.ID, "slow"); status != http.StatusGatewayTimeout {
		t.Fatalf("expected 504 timeout, got %d body=%s", status, body)
	}
	mustDialUser(t, env.ts.URL, room.ID, "user-1")
	mustDialUser(t, env.ts.URL, room.ID, "user-2")
	if _, status, body := dialUser(t, env.ts.URL, room.ID, "user-3"); status != http.StatusConflict {
		t.Fatalf("expected 409 full, got %d body=%s", status, body)
	}
}

func TestUnauthorizedAndPasswordChange(t *testing.T) {
	env := newTestEnv(t, nil)
	noCookies := &http.Client{Timeout: 5 * time.Second}
	resp, err := noCookies.Get(env.ts.URL + "/api/admin/overview")
	if err != nil {
		t.Fatalf("overview: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthorized overview status=%d", resp.StatusCode)
	}

	status, body := env.adminJSON(http.MethodPost, "/api/admin/login", map[string]any{"password": "wrong-pass"}, nil)
	if status != http.StatusUnauthorized {
		t.Fatalf("wrong login status=%d body=%s", status, body)
	}
	status, body = env.adminJSON(http.MethodPost, "/api/admin/setup", map[string]any{"password": "another-pass"}, nil)
	if status != http.StatusBadRequest {
		t.Fatalf("second setup status=%d body=%s", status, body)
	}

	status, body = env.adminJSON(http.MethodPut, "/api/admin/password", map[string]any{
		"current_password": "test-password",
		"new_password":     "new-test-password",
	}, nil)
	if status != http.StatusOK {
		t.Fatalf("change password status=%d body=%s", status, body)
	}
	env.adminJSON(http.MethodPost, "/api/admin/logout", nil, nil)
	status, body = env.adminJSON(http.MethodPost, "/api/admin/login", map[string]any{"password": "test-password"}, nil)
	if status != http.StatusUnauthorized {
		t.Fatalf("old password status=%d body=%s", status, body)
	}
	status, body = env.adminJSON(http.MethodPost, "/api/admin/login", map[string]any{"password": "new-test-password"}, nil)
	if status != http.StatusOK {
		t.Fatalf("new password status=%d body=%s", status, body)
	}
}

func TestForwardedInputSanitization(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/v1/rooms/r_x/ws?token=abc&x=1", nil)
	req.Header.Set("Cookie", "xr_admin=secret-session")
	req.Header.Set("Authorization", "Bearer user-token")
	req.Header.Set("X-Custom", "custom-value")

	headers := forwardedHeaders(req)
	if _, ok := headers["Cookie"]; ok {
		t.Fatalf("cookie header leaked: %+v", headers)
	}
	if got := headers["Authorization"]; len(got) == 0 {
		t.Fatalf("authorization header missing")
	}
	if got := headers["X-Custom"]; len(got) != 1 || got[0] != "custom-value" {
		t.Fatalf("custom header = %v", got)
	}
	query := forwardedQuery(req)
	if len(query["token"]) != 1 || query["token"][0] != "abc" {
		t.Fatalf("query = %+v", query)
	}
}
