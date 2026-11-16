// Command consumer is a runnable XiaoyuRepeater SDK example.
//
// It creates a room, keeps the agent channel open, allows users whose token
// query parameter matches -token, and closes the room on exit. The demo rule
// is illustrative; production consumers should validate their own credentials.
package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"gitee.com/PYLinTech/XiaoyuRepeater/sdk/go"
)

func main() {
	server := flag.String("server", "", "中继服务地址；默认读取 XR_SERVER 或 config.json")
	apiKey := flag.String("key", "", "API 密钥；默认读取 XR_API_KEY")
	roomName := flag.String("name", "演示房间", "房间名称")
	demoToken := flag.String("token", "demo-token", "演示放行令牌")
	maxParticipants := flag.Int("max", 0, "房间人数上限；0 表示使用中继默认值")
	flag.Parse()

	client, err := xiaoyurepeater.NewClient(*apiKey, *server)
	if err != nil {
		log.Fatalf("初始化 SDK 失败：%v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	created, err := client.CreateRoom(ctx, xiaoyurepeater.RoomOptions{
		Name:            *roomName,
		MaxParticipants: *maxParticipants,
	})
	if err != nil {
		log.Fatalf("创建房间失败：%v", err)
	}
	log.Printf("房间已创建：%s（%s）", created.Room.Name, created.Room.ID)

	joinValues := url.Values{
		"token": {*demoToken},
		"name":  {"Alice"},
	}
	joinURL, err := created.JoinWSURL(joinValues)
	if err == nil {
		log.Printf("用户加入地址（示例）：%s", joinURL)
	}
	if rtcURL, err := created.JoinWebRTCURL(joinValues); err == nil {
		log.Printf("WebRTC 信令地址（示例）：%s", rtcURL)
	}
	log.Printf("演示鉴权：query 中的 token 与启动参数一致时放行，name 作为控制台显示名。")

	roomClosed := make(chan struct{}, 1)
	handler := xiaoyurepeater.AgentHandler{
		OnHello: func(_ context.Context, _ *xiaoyurepeater.Agent, hello xiaoyurepeater.Hello) {
			log.Printf("鉴权通道已连接（协议 v%d，密钥「%s」）", hello.Protocol, hello.KeyName)
			for _, room := range hello.Rooms {
				log.Printf("  房间 %s「%s」：%d 个连接，%d 条轨道",
					room.ID, room.Name, len(room.Connections), len(room.Tracks))
			}
		},
		Authorize: func(_ context.Context, _ *xiaoyurepeater.Agent, request xiaoyurepeater.AuthRequest) xiaoyurepeater.AuthDecision {
			if first(request.Query["token"]) != *demoToken {
				return xiaoyurepeater.Deny("演示令牌不正确")
			}
			subject := first(request.Query["name"])
			if subject == "" {
				subject = "访客"
			}
			return xiaoyurepeater.Allow(subject)
		},
		OnUserJoined: func(_ context.Context, _ *xiaoyurepeater.Agent, event xiaoyurepeater.UserJoined) {
			log.Printf("用户「%s」加入房间「%s」（%s）", event.Subject, event.RoomName, event.Transport)
		},
		OnUserLeft: func(_ context.Context, _ *xiaoyurepeater.Agent, event xiaoyurepeater.UserLeft) {
			log.Printf("用户「%s」离开房间「%s」（%s）", event.Subject, event.RoomName, event.Reason)
		},
		OnRoomClosed: func(_ context.Context, _ *xiaoyurepeater.Agent, event xiaoyurepeater.RoomClosed) {
			log.Printf("房间「%s」已关闭（%s）", event.RoomName, event.Reason)
			if event.RoomID == created.Room.ID {
				select {
				case roomClosed <- struct{}{}:
				default:
				}
			}
		},
		OnTrackPublished: func(_ context.Context, _ *xiaoyurepeater.Agent, event xiaoyurepeater.TrackPublished) {
			log.Printf("轨道已发布：%s（%s，%s）", event.Track.ID, event.Track.Kind, event.Track.Codec)
		},
		OnTrackUnpublished: func(_ context.Context, _ *xiaoyurepeater.Agent, event xiaoyurepeater.TrackUnpublished) {
			log.Printf("轨道已结束：%s（%s）", event.Track.ID, event.Reason)
		},
		OnError: func(_ context.Context, _ *xiaoyurepeater.Agent, event xiaoyurepeater.AgentError) {
			log.Printf("控制通道错误（请求 %s）：%s", event.RequestID, event.Error)
		},
		OnDisconnect: func(err error, retryIn time.Duration) {
			if retryIn > 0 {
				log.Printf("鉴权通道断开：%v；%s 后重连", err, retryIn)
			} else {
				log.Printf("鉴权通道停止：%v", err)
			}
		},
	}

	agent := client.NewAgent(handler)
	go func() {
		select {
		case <-roomClosed:
			stop()
		case <-ctx.Done():
		}
	}()

	runErr := agent.Run(ctx)
	if runErr != nil && !errors.Is(runErr, context.Canceled) {
		log.Printf("鉴权通道退出：%v", runErr)
	}

	closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.CloseRoom(closeCtx, created.Room.ID); err != nil {
		log.Printf("关闭房间失败：%v", err)
	} else {
		log.Printf("已关闭中转房间")
	}
}

func first(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return strings.TrimSpace(values[0])
}
