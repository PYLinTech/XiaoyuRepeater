package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"xiaoyurepeater/internal/config"
	"xiaoyurepeater/internal/server"
	"xiaoyurepeater/internal/store"
	"xiaoyurepeater/internal/version"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "配置错误:", err)
		os.Exit(1)
	}
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(logger)

	st, err := store.Open(cfg.DataDir)
	if err != nil {
		logger.Error("打开数据存储失败", "error", err)
		os.Exit(1)
	}
	srv, err := server.New(cfg, st, logger)
	if err != nil {
		logger.Error("初始化服务失败", "error", err)
		os.Exit(1)
	}

	httpServer := &http.Server{
		Addr:              cfg.Addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	if srv.AdminSetupRequired() {
		logger.Info("首次启动：请打开控制台设置管理密码", "url", "http://"+displayAddr(cfg.Addr))
	}

	go func() {
		logger.Info("XiaoyuRepeater 已启动",
			"version", version.Version,
			"addr", cfg.Addr,
			"data_dir", cfg.DataDir,
			"tls", cfg.TLSCert != "")
		var listenErr error
		if cfg.TLSCert != "" {
			listenErr = httpServer.ListenAndServeTLS(cfg.TLSCert, cfg.TLSKey)
		} else {
			listenErr = httpServer.ListenAndServe()
		}
		if listenErr != nil && !errors.Is(listenErr, http.ErrServerClosed) {
			logger.Error("HTTP 服务退出", "error", listenErr)
			os.Exit(1)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	logger.Info("正在关闭服务")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(ctx); err != nil {
		logger.Warn("HTTP 关闭超时", "error", err)
	}
	srv.Close()
	logger.Info("服务已关闭")
}

func displayAddr(addr string) string {
	if len(addr) > 0 && addr[0] == ':' {
		return "localhost" + addr
	}
	return addr
}
