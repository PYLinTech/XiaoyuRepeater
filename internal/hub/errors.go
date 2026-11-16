package hub

import "fmt"

type Error struct {
	Status  int
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Message }

func hError(status int, code, message string) *Error {
	return &Error{Status: status, Code: code, Message: message}
}

var (
	ErrRoomNotFound      = hError(404, "room_not_found", "房间不存在")
	ErrRoomClosed        = hError(410, "room_closed", "房间已关闭")
	ErrRoomFull          = hError(409, "room_full", "房间人数已满")
	ErrAgentOffline      = hError(503, "agent_offline", "使用方程序未连接")
	ErrAuthTimeout       = hError(504, "auth_timeout", "使用方鉴权超时")
	ErrAuthDenied        = hError(403, "auth_denied", "鉴权未通过")
	ErrTooManyRooms      = hError(429, "too_many_rooms", "该密钥创建的房间数量已达上限")
	ErrAgentBusy         = hError(503, "agent_busy", "使用方鉴权请求过多，请稍后重试")
	ErrHubClosed         = hError(503, "server_shutting_down", "服务正在关闭")
	ErrInvalidRoomID     = hError(400, "invalid_room_id", "房间 ID 无效")
	ErrWebRTCUnavailable = hError(503, "webrtc_unavailable", "WebRTC 数据面未启用")
)

func denied(reason string) *Error {
	msg := "鉴权未通过"
	if reason != "" {
		msg = fmt.Sprintf("鉴权未通过：%s", reason)
	}
	return &Error{Status: 403, Code: "auth_denied", Message: msg}
}
