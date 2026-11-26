// Command agy2api 启动网关。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/shiceai/agy2api/internal/config"
	"github.com/shiceai/agy2api/internal/cryptox"
	"github.com/shiceai/agy2api/internal/logx"
	"github.com/shiceai/agy2api/internal/mockupstream"
	"github.com/shiceai/agy2api/internal/server"
	"github.com/shiceai/agy2api/internal/store"
	"github.com/shiceai/agy2api/internal/upgrade"
)

// version 由构建脚本通过 -ldflags "-X main.version=..." 注入。
var version = "dev"

func main() {
	showVersion := flag.Bool("version", false, "打印版本号后退出")
	flag.Parse()
	if *showVersion {
		fmt.Println(version)
		return
	}

	cfg := config.Load()
	cfg.Version = version

	level, levelErr := logx.ParseLevel(cfg.LogLevel)
	slog.SetDefault(logx.New(os.Stderr, level))
	if levelErr != nil {
		slog.Warn("AGY2API_LOG_LEVEL 无法识别，使用 info", "value", cfg.LogLevel)
	}

	// 数据目录里有在线升级的更新的程序时，把进程换成它（不返回）。
	upgrade.Boot(cfg.DataDir, version, cfg.Update)
	if base, ok := upgrade.Running(); ok {
		slog.Info("运行在线升级的程序", "version", version, "base", base)
	}

	if run(cfg) {
		// 在线升级或撤销：服务和数据库都关了，换回自带的程序，由它选择运行哪个。
		if err := upgrade.Restart(); err != nil {
			fatal("重启失败", "err", err)
		}
	}
}

// run 启动服务，直到收到退出信号或需要重启（在线升级、撤销），关停后返回是否要重启。
func run(cfg config.Config) (restart bool) {
	// 开发联调：在同进程内另起一个模拟上游，并把令牌/上游地址指向它。
	if cfg.EmbedMock {
		base := startMockUpstream(cfg.MockAddr)
		cfg.TokenURL = base + "/token"
		cfg.UserInfoURL = base + "/oauth2/v2/userinfo"
		cfg.UpstreamBases = []string{base}
		slog.Info("模拟上游已启动", "base", base)
	}

	if cfg.AdminPassword == "" && !cfg.AllowNoAdminAuth {
		fatal("AGY2API_ADMIN_PASSWORD 未设置；本机开发可设 AGY2API_ALLOW_NO_ADMIN_AUTH=true 跳过管理端鉴权")
	}
	if cfg.AdminPassword != "" && !cfg.AllowNoAdminAuth {
		if weak := config.AdminPasswordWeakness(cfg.AdminPassword); weak != "" {
			slog.Warn("管理口令太弱，上公网前换成足够长的随机串（如 openssl rand -base64 24）", "reason", weak)
		}
	}

	keyMaterial, err := cryptox.LoadOrCreateKey(cfg.EncryptionKey, cfg.DataDir)
	if err != nil {
		fatal("加载加密密钥失败", "err", err)
	}
	enc, err := cryptox.New(keyMaterial)
	if err != nil {
		fatal("初始化加密失败", "err", err)
	}

	st, err := store.Open(cfg.DataDir)
	if err != nil {
		fatal("打开数据库失败", "err", err)
	}
	defer st.Close()
	if unknown := st.UnknownMigrations(); len(unknown) > 0 {
		slog.Warn("数据库被更新的版本改过结构，当前程序可能不兼容；请换回新版本，或恢复升级前的备份", "migrations", unknown)
	}

	svc, err := server.NewService(context.Background(), cfg, st, enc)
	if err != nil {
		fatal("初始化服务失败", "err", err)
	}

	srv := &http.Server{
		Handler:           svc.Router(),
		ReadHeaderTimeout: 15 * time.Second,
		// 不设 IdleTimeout 时，ReadTimeout 为 0 的 keep-alive 空闲连接永不超时。
		IdleTimeout: 2 * time.Minute,
		// 不设 ReadTimeout、WriteTimeout：读超时到期会取消请求 ctx，写超时会截断流式响应；
		// 请求体大小由 MaxBytesReader 限制，上游调用按请求 ctx 控制截止时间。
	}

	// 每个地址各开一个监听，共用同一个服务；有一个监听不了就不启动（只绑上一部分时，漏掉的地址不好发现）。
	listeners := make([]net.Listener, 0, len(cfg.Addrs))
	for _, addr := range cfg.Addrs {
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			for _, l := range listeners {
				_ = l.Close()
			}
			fatal("监听失败", "addr", addr, "err", err)
		}
		listeners = append(listeners, ln)
	}
	slog.Info("agy2api 监听", "version", version, "addr", strings.Join(cfg.Addrs, ", "), "data_dir", cfg.DataDir)
	// 能监听了才算在线升级的程序正常启动；之前退出的都计入启动失败次数。
	upgrade.Confirm(cfg.DataDir)
	for _, ln := range listeners {
		go func() {
			if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
				fatal("服务退出", "addr", ln.Addr().String(), "err", err)
			}
		}()
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	select {
	case <-stop:
		slog.Info("正在关闭")
	case <-svc.RestartRequested():
		restart = true
		slog.Info("正在关闭，之后重启")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		slog.Warn("等待进行中的请求结束超时", "err", err)
	}
	// 请求都结束后再关服务，写完排队的用量记录，之后才关数据库（defer）。
	closeCtx, closeCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer closeCancel()
	if err := svc.Close(closeCtx); err != nil {
		slog.Error("关闭时用量记录没写完", "err", err)
	}
	// 重启前关停的这段时间里收到了退出信号（如 docker stop）：按退出处理，不再重启。
	select {
	case <-stop:
		return false
	default:
		return restart
	}
}

// fatal 记一条错误日志后退出进程。
func fatal(msg string, args ...any) {
	slog.Error(msg, args...)
	os.Exit(1)
}

// startMockUpstream 在 addr 上启动模拟上游，返回其 base URL。
func startMockUpstream(addr string) string {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		fatal("启动模拟上游失败", "err", err)
	}
	go func() {
		_ = http.Serve(ln, mockupstream.Handler())
	}()
	return "http://" + ln.Addr().String()
}
