// 使用方示例：创建中转房间，保持鉴权通道，并按 demo 规则放行用户。
//
//	go run ./examples/consumer -server http://127.0.0.1:8080 -key <API密钥>
//
// 可以通过环境变量 XR_SERVER / XR_API_KEY / XR_ROOM_NAME / XR_DEMO_TOKEN 配置。
package main

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/websocket"

	"xiaoyurepeater/internal/protocol"
)

var errRoomClosed = errors.New("房间已关闭")

type options struct {
	server          string
	apiKey          string
	roomName        string
	demoToken       string
	maxParticipants int
}

type createdRoom struct {
	Protocol int `json:"protocol"`
	Room     struct {
		ID              string `json:"id"`
		Name            string `json:"name"`
		MaxParticipants int    `json:"max_participants"`
	} `json:"room"`
	WSPath    string `json:"ws_path"`
	WSURL     string `json:"ws_url"`
	WebRTCURL string `json:"webrtc_url"`
}

func main() {
	opts := options{}
	flag.StringVar(&opts.server, "server", envOr("XR_SERVER", "http://127.0.0.1:8080"), "中继服务地址")
	flag.StringVar(&opts.apiKey, "key", os.Getenv("XR_API_KEY"), "API 密钥（也可用 XR_API_KEY）")
	flag.StringVar(&opts.roomName, "name", envOr("XR_ROOM_NAME", "演示房间"), "房间名称")
	flag.StringVar(&opts.demoToken, "token", envOr("XR_DEMO_TOKEN", "demo-token"), "演示鉴权令牌")
	flag.IntVar(&opts.maxParticipants, "max", 20, "房间人数上限")
	flag.Parse()

	if strings.TrimSpace(opts.apiKey) == "" {
		log.Fatal("缺少 API 密钥：请在控制台创建密钥，然后用 -key 或 XR_API_KEY 传入")
	}
	if strings.TrimSpace(opts.demoToken) == "" {
		log.Fatal("演示令牌不能为空")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, opts); err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context, opts options) error {
	base, err := url.Parse(strings.TrimRight(opts.server, "/"))
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") {
		return fmt.Errorf("服务地址无效，需要以 http:// 或 https:// 开头")
	}

	room, err := createRoom(ctx, base, opts)
	if err != nil {
		return err
	}
	log.Printf("房间已创建：%s（%s）", room.Room.Name, room.Room.ID)

	joinURL, err := withQuery(room.WSURL, url.Values{
		"token": {opts.demoToken},
		"name":  {"Alice"},
	})
	if err != nil {
		return fmt.Errorf("生成加入地址失败：%w", err)
	}
	log.Printf("用户加入地址（示例）：%s", joinURL)
	if room.WebRTCURL != "" {
		rtcURL, err := withQuery(room.WebRTCURL, url.Values{
			"token": {opts.demoToken},
			"name":  {"Alice"},
		})
		if err == nil {
			log.Printf("WebRTC 信令地址（示例）：%s", rtcURL)
		}
	}
	log.Printf("鉴权规则：token 等于演示令牌时放行，name 作为显示名。")
	log.Printf("数据面由房间内用户直接收发 WS 文本/二进制帧，中继原样转发，不解析内容。")

	agentErr := agentLoop(ctx, base, opts, room)
	if errors.Is(agentErr, errRoomClosed) {
		log.Printf("房间已被关闭，演示结束")
		return nil
	}

	closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := closeRoom(closeCtx, base, opts.apiKey, room.Room.ID); err != nil {
		log.Printf("关闭房间失败：%v", err)
	} else {
		log.Printf("已关闭中转房间")
	}
	return nil
}

func createRoom(ctx context.Context, base *url.URL, opts options) (createdRoom, error) {
	var room createdRoom
	body, err := json.Marshal(map[string]any{
		"name":             opts.roomName,
		"max_participants": opts.maxParticipants,
	})
	if err != nil {
		return room, err
	}

	endpoint := *base
	endpoint.Path = strings.TrimRight(endpoint.Path, "/") + "/v1/rooms"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return room, err
	}
	req.Header.Set("Authorization", "Bearer "+opts.apiKey)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return room, fmt.Errorf("连接中继服务失败：%w", err)
	}
	defer resp.Body.Close()
	payload, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusCreated {
		return room, fmt.Errorf("创建房间失败（HTTP %d）：%s", resp.StatusCode, apiMessage(payload))
	}
	if err := json.Unmarshal(payload, &room); err != nil {
		return room, fmt.Errorf("解析创建房间响应失败：%w", err)
	}
	return room, nil
}

func closeRoom(ctx context.Context, base *url.URL, apiKey, roomID string) error {
	endpoint := *base
	endpoint.Path = strings.TrimRight(endpoint.Path, "/") + "/v1/rooms/" + url.PathEscape(roomID)
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, endpoint.String(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := (&http.Client{Timeout: 8 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil
	}
	if resp.StatusCode != http.StatusOK {
		payload, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		return fmt.Errorf("HTTP %d：%s", resp.StatusCode, apiMessage(payload))
	}
	return nil
}

func agentLoop(ctx context.Context, base *url.URL, opts options, room createdRoom) error {
	agentURL := *base
	if agentURL.Scheme == "https" {
		agentURL.Scheme = "wss"
	} else {
		agentURL.Scheme = "ws"
	}
	agentURL.Path = strings.TrimRight(agentURL.Path, "/") + "/v1/agent"
	agentURL.RawQuery = ""

	backoff := time.Second
	for {
		if ctx.Err() != nil {
			return nil
		}
		err := runAgent(ctx, agentURL.String(), opts, room)
		if ctx.Err() != nil {
			return nil
		}
		if errors.Is(err, errRoomClosed) {
			return err
		}
		log.Printf("使用方通道断开：%v", err)
		log.Printf("%s 后尝试重连…", backoff)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
			if backoff > 30*time.Second {
				backoff = 30 * time.Second
			}
		}
	}
}

func runAgent(ctx context.Context, endpoint string, opts options, room createdRoom) error {
	dialer := websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
		ReadBufferSize:   4096,
		WriteBufferSize:  4096,
	}
	header := http.Header{}
	header.Set("Authorization", "Bearer "+opts.apiKey)
	ws, resp, err := dialer.DialContext(ctx, endpoint, header)
	if err != nil {
		if resp != nil {
			return fmt.Errorf("连接鉴权通道失败（HTTP %d）", resp.StatusCode)
		}
		return fmt.Errorf("连接鉴权通道失败：%w", err)
	}
	defer ws.Close()
	ws.SetReadLimit(512 << 10)

	connCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-connCtx.Done():
			_ = ws.Close()
		case <-done:
		}
	}()

	var writeMu sync.Mutex
	writeJSON := func(value any) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		_ = ws.SetWriteDeadline(time.Now().Add(10 * time.Second))
		return ws.WriteJSON(value)
	}

	log.Printf("使用方通道已连接，等待鉴权请求…")

	for {
		_, data, err := ws.ReadMessage()
		if err != nil {
			return fmt.Errorf("读取通道失败：%w", err)
		}

		var envelope struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(data, &envelope); err != nil {
			log.Printf("收到无法解析的消息，已忽略")
			continue
		}

		switch envelope.Type {
		case protocol.TypeHello:
			var hello protocol.Hello
			if err := json.Unmarshal(data, &hello); err != nil {
				return fmt.Errorf("解析 hello 失败：%w", err)
			}
			log.Printf("中继握手完成：协议 v%d，密钥「%s」，已有 %d 个房间",
				hello.Protocol, hello.KeyName, len(hello.Rooms))
			for _, ref := range hello.Rooms {
				log.Printf("  - 房间 %s「%s」%d 位用户", ref.ID, ref.Name, ref.Participants)
			}
		case protocol.TypeAuthRequest:
			var req protocol.AuthRequest
			if err := json.Unmarshal(data, &req); err != nil {
				log.Printf("解析鉴权请求失败：%v", err)
				continue
			}
			resp := decideAuth(req, opts.demoToken)
			if resp.Allow != nil && *resp.Allow {
				log.Printf("放行用户「%s」（%s，%s，房间「%s」）",
					resp.Subject, transportLabel(req.Transport), req.RemoteAddr, req.RoomName)
			} else {
				log.Printf("拒绝用户（%s，房间「%s」）：%s",
					transportLabel(req.Transport), req.RoomName, resp.Reason)
			}
			if err := writeJSON(resp); err != nil {
				return fmt.Errorf("发送鉴权结果失败：%w", err)
			}
		case protocol.TypeUserJoined:
			var event protocol.UserJoined
			if json.Unmarshal(data, &event) == nil {
				log.Printf("用户「%s」通过 %s 加入了房间「%s」",
					event.Subject, transportLabel(event.Transport), event.RoomName)
			}
		case protocol.TypeUserLeft:
			var event protocol.UserLeft
			if json.Unmarshal(data, &event) == nil {
				log.Printf("用户「%s」离开了房间「%s」（%s，%s）",
					event.Subject, event.RoomName, transportLabel(event.Transport), event.Reason)
			}
		case protocol.TypeRoomClosed:
			var event protocol.RoomClosed
			if json.Unmarshal(data, &event) == nil {
				log.Printf("房间「%s」已关闭（%s）", event.RoomName, event.Reason)
			}
			return errRoomClosed
		case protocol.TypePing:
			_ = writeJSON(protocol.Pong{Type: protocol.TypePong, Time: time.Now()})
		default:
			log.Printf("收到未知消息类型 %q，已忽略", envelope.Type)
		}
	}
}

func decideAuth(req protocol.AuthRequest, demoToken string) protocol.AuthResponse {
	allow := false
	subject := firstValue(req.Query["name"])
	if subject == "" {
		subject = "访客"
	}
	if runes := []rune(subject); len(runes) > 32 {
		subject = string(runes[:32])
	}
	reason := ""
	token := firstValue(req.Query["token"])
	if subtle.ConstantTimeCompare([]byte(token), []byte(demoToken)) != 1 {
		reason = "演示令牌不正确"
		subject = ""
	} else {
		allow = true
	}
	return protocol.AuthResponse{
		Type:      protocol.TypeAuthResponse,
		RequestID: req.RequestID,
		Allow:     &allow,
		Subject:   subject,
		Reason:    reason,
	}
}

func withQuery(raw string, values url.Values) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	query := u.Query()
	for key, items := range values {
		for _, item := range items {
			query.Set(key, item)
		}
	}
	u.RawQuery = query.Encode()
	return u.String(), nil
}

func apiMessage(payload []byte) string {
	var data struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(payload, &data) == nil && data.Error != "" {
		return data.Error
	}
	text := strings.TrimSpace(string(payload))
	if text == "" {
		return "无响应内容"
	}
	if len([]rune(text)) > 160 {
		return string([]rune(text)[:160])
	}
	return text
}

func firstValue(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return strings.TrimSpace(values[0])
}

func transportLabel(transport string) string {
	if transport == protocol.TransportWebRTC {
		return "WebRTC"
	}
	return "WS"
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
