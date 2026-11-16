package hub

import (
	"fmt"
	"sync"
	"time"
)

type Event struct {
	ID      int64     `json:"id"`
	Time    time.Time `json:"time"`
	Kind    string    `json:"kind"`
	Message string    `json:"message"`
	RoomID  string    `json:"room_id,omitempty"`
	Subject string    `json:"subject,omitempty"`
}

type eventLog struct {
	mu     sync.Mutex
	nextID int64
	items  []Event
	limit  int
}

func newEventLog(limit int) *eventLog {
	return &eventLog{limit: limit}
}

func (l *eventLog) add(kind, message, roomID, subject string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.nextID++
	l.items = append(l.items, Event{
		ID:      l.nextID,
		Time:    time.Now().UTC(),
		Kind:    kind,
		Message: message,
		RoomID:  roomID,
		Subject: subject,
	})
	if len(l.items) > l.limit {
		l.items = l.items[len(l.items)-l.limit:]
	}
}

func (l *eventLog) list() []Event {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]Event, len(l.items))
	for i := range l.items {
		out[len(l.items)-1-i] = l.items[i]
	}
	return out
}

func eventRoomCreated(name string) string {
	return fmt.Sprintf("房间「%s」已创建", name)
}

func eventRoomClosed(name, reason string) string {
	label := map[string]string{
		"closed_by_admin":    "管理员关闭",
		"closed_by_consumer": "使用方关闭",
		"key_revoked":        "密钥已撤销",
		"server_shutdown":    "服务关闭",
		"room_replaced":      "房间被替换",
	}[reason]
	if label == "" {
		label = reason
	}
	return fmt.Sprintf("房间「%s」已关闭（%s）", name, label)
}
