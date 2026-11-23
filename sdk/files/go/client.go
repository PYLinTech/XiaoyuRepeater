// Package xiaoyurepeater is the consumer-side SDK for XiaoyuRepeater.
//
// It covers the control plane only: API-key room management, the real-time
// agent channel, authorization callbacks, room/participant events and WebRTC
// subscription commands. User payload bytes and RTP media never pass through
// this SDK.
package xiaoyurepeater

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const maxResponseBytes = 4 << 20

// APIError is a non-2xx response or a transport-level API failure.
type APIError struct {
	Status  int
	Code    string
	Message string
	Detail  string
}

func (e *APIError) Error() string {
	parts := []string{e.Message}
	if e.Code != "" {
		parts = append(parts, "("+e.Code+")")
	}
	if e.Detail != "" {
		parts = append(parts, e.Detail)
	}
	return strings.Join(parts, " ")
}

// Client is an API-key-scoped consumer client.
type Client struct {
	BaseURL string
	APIKey  string
	HTTP    *http.Client
}

// NewClient builds a client. Empty apiKey / baseURL fall back to XR_API_KEY
// and, for the base URL, XR_SERVER followed by the shared config.json.
func NewClient(apiKey, baseURL string) (*Client, error) {
	if apiKey == "" {
		apiKey = strings.TrimSpace(os.Getenv("XR_API_KEY"))
	}
	if apiKey == "" {
		return nil, errors.New("缺少 API Key：设置 XR_API_KEY 或传入 apiKey")
	}
	resolved, err := resolveBaseURL(baseURL)
	if err != nil {
		return nil, err
	}
	return &Client{
		BaseURL: resolved,
		APIKey:  apiKey,
		HTTP:    &http.Client{Timeout: 30 * time.Second},
	}, nil
}

// SDKVersion queries the public SDK metadata endpoint without an API key.
func SDKVersion(ctx context.Context, baseURL string) (VersionInfo, error) {
	resolved, err := resolveBaseURL(baseURL)
	if err != nil {
		return VersionInfo{}, err
	}
	client := &http.Client{Timeout: 15 * time.Second}
	var out VersionInfo
	err = requestJSON(ctx, client, resolved, "", http.MethodGet, "/api/sdk/version", nil, &out)
	return out, err
}

// SDKVersion queries the public SDK metadata endpoint for this client.
func (c *Client) SDKVersion(ctx context.Context) (VersionInfo, error) {
	return SDKVersion(ctx, c.BaseURL)
}

// CreateRoom creates a relay room owned by this API key.
func (c *Client) CreateRoom(ctx context.Context, options RoomOptions) (CreatedRoom, error) {
	var out CreatedRoom
	err := c.do(ctx, http.MethodPost, "/v1/rooms", options, &out)
	return out, err
}

// ListRooms lists all live rooms owned by this API key.
func (c *Client) ListRooms(ctx context.Context) ([]Room, error) {
	var out struct {
		Rooms []Room `json:"rooms"`
	}
	err := c.do(ctx, http.MethodGet, "/v1/rooms", nil, &out)
	return out.Rooms, err
}

// GetRoom returns one live room owned by this API key.
func (c *Client) GetRoom(ctx context.Context, roomID string) (Room, error) {
	var out struct {
		Room Room `json:"room"`
	}
	err := c.do(ctx, http.MethodGet, "/v1/rooms/"+url.PathEscape(roomID), nil, &out)
	return out.Room, err
}

// CloseRoom closes a room. A room that is already gone is treated as success,
// which makes cleanup idempotent.
func (c *Client) CloseRoom(ctx context.Context, roomID string) error {
	err := c.do(ctx, http.MethodDelete, "/v1/rooms/"+url.PathEscape(roomID), nil, nil)
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound {
		return nil
	}
	return err
}

// AgentURL returns the WebSocket URL of the agent channel.
func (c *Client) AgentURL() (string, error) {
	return webSocketURL(c.BaseURL, "/v1/agent")
}

// JoinWSURL appends join parameters to the room's WebSocket URL.
func (r CreatedRoom) JoinWSURL(values url.Values) (string, error) {
	return JoinURL(r.WSURL, values)
}

// JoinWebRTCURL appends join parameters to the room's WebRTC signaling URL.
func (r CreatedRoom) JoinWebRTCURL(values url.Values) (string, error) {
	if r.WebRTCURL == "" {
		return "", ErrWebRTCUnavailable
	}
	return JoinURL(r.WebRTCURL, values)
}

// JoinURL adds join parameters to a ws:// or wss:// room URL.
func JoinURL(rawURL string, values url.Values) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return "", fmt.Errorf("解析房间地址失败: %w", err)
	}
	if parsed.Scheme != "ws" && parsed.Scheme != "wss" {
		return "", errors.New("房间地址必须以 ws:// 或 wss:// 开头")
	}
	query := parsed.Query()
	for key, items := range values {
		query.Del(key)
		for _, item := range items {
			query.Add(key, item)
		}
	}
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func (c *Client) do(ctx context.Context, method, endpoint string, body any, out any) error {
	return requestJSON(ctx, c.HTTP, c.BaseURL, c.APIKey, method, endpoint, body, out)
}

func requestJSON(ctx context.Context, httpClient *http.Client, baseURL, apiKey, method, endpoint string, body, out any) error {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, baseURL+endpoint, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return err
	}
	if len(raw) > maxResponseBytes {
		return errors.New("服务端响应过大")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return apiErrorFromBody(resp.StatusCode, raw)
	}
	if out == nil {
		return nil
	}
	if len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("解析服务端响应失败: %w", err)
	}
	return nil
}

func apiErrorFromBody(status int, raw []byte) *APIError {
	var payload struct {
		Error string `json:"error"`
		Code  string `json:"code"`
	}
	_ = json.Unmarshal(raw, &payload)
	message := payload.Error
	if message == "" {
		message = fmt.Sprintf("请求失败（HTTP %d）", status)
	}
	detail := ""
	trimmed := strings.TrimSpace(string(raw))
	if payload.Error == "" {
		detail = truncate(trimmed, 300)
	}
	return &APIError{Status: status, Code: payload.Code, Message: message, Detail: detail}
}

func resolveBaseURL(baseURL string) (string, error) {
	if baseURL == "" {
		baseURL = strings.TrimSpace(os.Getenv("XR_SERVER"))
	}
	if baseURL == "" {
		found, err := loadBaseURLFromConfig()
		if err != nil {
			return "", err
		}
		baseURL = found
	}
	return normalizeBaseURL(baseURL)
}

func normalizeBaseURL(raw string) (string, error) {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return "", errors.New("服务地址必须以 http:// 或 https:// 开头")
	}
	return raw, nil
}

func loadBaseURLFromConfig() (string, error) {
	var candidates []string
	if _, file, _, ok := runtime.Caller(0); ok {
		candidates = append(candidates, filepath.Join(filepath.Dir(file), "..", "config.json"))
	}
	if workdir, err := os.Getwd(); err == nil {
		candidates = append(candidates,
			filepath.Join(workdir, "config.json"),
			filepath.Join(workdir, "..", "config.json"),
		)
	}
	seen := make(map[string]bool, len(candidates))
	for _, candidate := range candidates {
		if seen[candidate] {
			continue
		}
		seen[candidate] = true
		raw, err := os.ReadFile(candidate)
		if err != nil {
			continue
		}
		var cfg struct {
			BaseURL string `json:"baseUrl"`
		}
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return "", fmt.Errorf("解析 %s 失败: %w", candidate, err)
		}
		if strings.TrimSpace(cfg.BaseURL) == "" {
			return "", fmt.Errorf("%s 缺少 baseUrl", candidate)
		}
		return cfg.BaseURL, nil
	}
	return "", errors.New("未找到 config.json，请设置 XR_SERVER 或传入 baseURL")
}

func webSocketURL(baseURL, endpoint string) (string, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return "", fmt.Errorf("解析服务地址失败: %w", err)
	}
	switch parsed.Scheme {
	case "https":
		parsed.Scheme = "wss"
	case "http":
		parsed.Scheme = "ws"
	default:
		return "", errors.New("服务地址必须以 http:// 或 https:// 开头")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + endpoint
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String(), nil
}

func truncate(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + "…"
}
