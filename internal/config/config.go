// Package config Agent 运行配置。
// 默认值面向生产:Unix Socket 于 /run/dockorae/agent.sock,组 dockorae,数据目录 /var/lib/dockorae-agent。
package config

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const (
	DefaultSocketPath = "/run/dockorae/agent.sock"
	DefaultGroup      = "dockorae"
	DefaultDataDir    = "/var/lib/dockorae-agent"
	DefaultLogDir     = "/var/log/dockorae"
	// DefaultTokenFile 面板自动生成的 token 落盘位置(面板与 Agent 共享该目录)
	DefaultTokenFile = "/run/dockorae/agent.token"
)

// Config Agent 配置
type Config struct {
	SocketPath  string // Unix socket 路径
	SocketDir   string // socket 所在目录(自动创建,0750)
	SocketGroup string // socket 组(0660 root:group)
	Token       string // 共享认证 token(空 = 不启用 token 认证,仅开发模式)
	TokenFile   string // token 文件(agent 启动时读取;命令行 token 优先)
	DataDir     string // 状态数据目录(compose digest 记录、binary 状态等)
	LogDir      string // 审计日志目录
	ComposeDir  string // 面板宿主 compose 目录(AGENT_COMPOSE_DIR;面板更新用,默认 /opt/docker-manager)
	InContainer bool   // 容器内运行(经 nsenter 操作宿主)
	ComposeBin  string // compose 命令("docker compose" 或 "docker-compose";默认自动探测)
}

// Load 从环境变量 + 命令行参数组装配置
func Load(socket, token, dataDir, logDir string, hostMode bool) *Config {
	c := &Config{
		SocketPath:  firstNonEmpty(os.Getenv("AGENT_SOCKET"), socket, DefaultSocketPath),
		SocketGroup: firstNonEmpty(os.Getenv("AGENT_GROUP"), DefaultGroup),
		DataDir:     firstNonEmpty(os.Getenv("AGENT_DATA_DIR"), dataDir, DefaultDataDir),
		LogDir:      firstNonEmpty(os.Getenv("AGENT_LOG_DIR"), logDir, DefaultLogDir),
		ComposeDir:  firstNonEmpty(os.Getenv("AGENT_COMPOSE_DIR"), "/opt/docker-manager"),
		ComposeBin:  os.Getenv("AGENT_COMPOSE_BIN"),
	}
	if t := os.Getenv("AGENT_TOKEN"); t != "" {
		c.Token = t
	} else if token != "" {
		c.Token = token
	}
	c.TokenFile = firstNonEmpty(os.Getenv("AGENT_TOKEN_FILE"), DefaultTokenFile)
	c.SocketDir = filepath.Dir(c.SocketPath)
	c.InContainer = !hostMode && detectContainer()
	return c
}

// firstNonEmpty 返回第一个非空值;全部为空返回最后一个(默认值)
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	if len(vals) == 0 {
		return ""
	}
	return vals[len(vals)-1]
}

// detectContainer 容器检测:/.dockerenv 或 cgroup 含 docker/kubepods 段
func detectContainer() bool {
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return true
	}
	raw, err := os.ReadFile("/proc/self/cgroup")
	if err == nil {
		s := string(raw)
		if strings.Contains(s, "docker") || strings.Contains(s, "kubepods") || strings.Contains(s, "containerd") {
			return true
		}
	}
	return false
}

// ModeLabel 运行模式标签(日志/审计用)
func (c *Config) ModeLabel() string {
	if c.InContainer {
		return "container+nsenter"
	}
	return "host-direct"
}

// Prepare 创建目录并解析 token 文件(无 token 时报错,除非明确允许无认证)
func (c *Config) Prepare() error {
	if err := os.MkdirAll(c.SocketDir, 0o750); err != nil {
		return err
	}
	if err := os.MkdirAll(c.DataDir, 0o700); err != nil {
		return err
	}
	if err := os.MkdirAll(c.LogDir, 0o700); err != nil {
		return err
	}
	if c.Token == "" && c.TokenFile != "" {
		if b, err := os.ReadFile(c.TokenFile); err == nil {
			c.Token = strings.TrimSpace(string(b))
		}
	}
	if c.Token == "" {
		// 无 token:拒绝启动(安全原则,防止裸 socket 无人看守)
		return os.ErrPermission
	}
	if c.ComposeBin == "" {
		c.ComposeBin = detectComposeBin()
	}
	return nil
}

// detectComposeBin 探测 compose 命令:优先 docker compose v2 插件,回退 docker-compose 独立二进制
func detectComposeBin() string {
	if _, err := exec.LookPath("docker"); err == nil {
		return "docker compose"
	}
	if _, err := exec.LookPath("docker-compose"); err == nil {
		return "docker-compose"
	}
	return "docker compose"
}
