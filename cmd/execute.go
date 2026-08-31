package cmd

import (
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/DockOrae/DockOrae-Agent/internal/apiserver"
	"github.com/DockOrae/DockOrae-Agent/internal/binary"
	"github.com/DockOrae/DockOrae-Agent/internal/config"
	"github.com/DockOrae/DockOrae-Agent/internal/files"
)

// Execute Agent 启动入口(极薄,仅解析参数并拉起 API 服务)
func Execute() {
	// 隐藏子命令:fsop(宿主文件操作,nsenter 模式下由 Agent 重新执行自身进入宿主命名空间)
	if len(os.Args) > 1 && os.Args[1] == "fsop" {
		os.Exit(files.FsopMain(os.Args[2:]))
	}

	fs := flag.NewFlagSet("dockorae-agent", flag.ExitOnError)
	showVersion := fs.Bool("version", false, "打印版本并退出")
	socket := fs.String("socket", "", "Unix socket 路径(默认 /run/dockorae/agent.sock)")
	token := fs.String("token", "", "共享认证 token(默认读 /run/dockorae/agent.token)")
	dataDir := fs.String("data", "", "状态数据目录(默认 /var/lib/dockorae-agent)")
	logDir := fs.String("log", "", "日志目录(默认 /var/log/dockorae)")
	hostMode := fs.Bool("host", false, "强制直连模式(非容器,不经 nsenter)")
	_ = fs.Parse(os.Args[1:])

	if *showVersion {
		fmt.Printf("dockorae-agent %s (commit %s, built %s)\n", DisplayVersion(), Commit, BuildTime)
		return
	}

	// 版本注入 binary 模块(避免 import cycle)
	binary.Version = Version

	cfg := config.Load(*socket, *token, *dataDir, *logDir, *hostMode)
	if err := cfg.Prepare(); err != nil {
		log.Fatalf("config: %v", err)
	}

	srv, err := apiserver.New(cfg)
	if err != nil {
		log.Fatalf("apiserver: %v", err)
	}
	srv.Version, srv.Commit, srv.BuildTime = Version, Commit, BuildTime
	log.Printf("dockorae-agent %s listening on %s (mode=%s)", DisplayVersion(), cfg.SocketPath, cfg.ModeLabel())
	if err := srv.Run(); err != nil {
		log.Fatalf("server: %v", err)
	}
}
