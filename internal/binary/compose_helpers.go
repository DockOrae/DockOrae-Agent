package binary

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"

	"github.com/DockOrae/DockOrae-Agent/internal/errs"
)

// Version 当前版本(与 cmd.Version 同步,由 cmd.Execute 注入)
var Version string

// cmdVersion 返回版本
func cmdVersion() string { return Version }

// newSHA256 SHA256 hasher
func newSHA256() interface {
	Write([]byte) (int, error)
	Sum([]byte) []byte
} {
	return sha256.New()
}

func sha256Hex(b []byte) string { return hex.EncodeToString(b) }

// ---------- compose 镜像 tag 工具(与 DockOrae 主程序同款逻辑) ----------

var imageLineRe = regexp.MustCompile(`^\s*image:\s*(\S+)\s*$`)

// findManagerImage 在 compose yaml 中查找 dockorae-agent 的 image 值(第一个匹配)。
// 只匹配镜像名(最后一段)为 dockorae-agent 的条目,绝不误改其他镜像。
func findManagerImage(yaml string) (string, bool) {
	for _, line := range strings.Split(yaml, "\n") {
		m := imageLineRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		val := m[1]
		name := val
		if i := strings.IndexAny(name, "@:"); i >= 0 {
			name = name[:i]
		}
		if i := strings.LastIndex(name, "/"); i >= 0 {
			name = name[i+1:]
		}
		if name == "dockorae-agent" {
			return val, true
		}
	}
	return "", false
}

// retagImageValue 把 image 值替换为指定 tag,保留 repository。
func retagImageValue(val, tag string) string {
	if i := strings.IndexAny(val, "@:"); i >= 0 {
		return val[:i] + ":" + tag
	}
	return val + ":" + tag
}

// safeImageValue 校验 image 值仅含安全字符
func safeImageValue(v string) bool {
	if v == "" {
		return false
	}
	for _, r := range v {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') ||
			r == '.' || r == '_' || r == ':' || r == '@' || r == '/' || r == '-' {
			continue
		}
		return false
	}
	return true
}

// ---------- compose 目录定位 ----------

// findComposeDir 定位宿主 compose 文件目录(容器模式):
//  1. AGENT_COMPOSE_DIR 环境变量显式指定
//  2. 经 docker.sock 检查 dockorae 面板容器 /data 挂载源反推
//  3. 默认 /opt/docker-manager
func (s *State) findComposeDir(ctx context.Context) (string, error) {
	if d := os.Getenv("AGENT_COMPOSE_DIR"); d != "" {
		return d, nil
	}
	cli, err := s.dockerClient()
	if err == nil {
		res, err := cli.ContainerList(ctx, client.ContainerListOptions{All: true})
		if err == nil {
			for _, c := range res.Items {
				name := ""
				if len(c.Names) > 0 {
					name = strings.TrimPrefix(c.Names[0], "/")
				}
				if name == "dockorae" {
					inj, err := cli.ContainerInspect(ctx, c.ID, client.ContainerInspectOptions{})
					if err == nil {
						for _, m := range inj.Container.Mounts {
							if m.Destination == "/data" && m.Source != "" {
								return filepath.Dir(m.Source), nil
							}
						}
					}
				}
			}
		}
	}
	// 默认安装目录
	if _, err := os.Stat("/opt/docker-manager/docker-compose.yml"); err == nil {
		return "/opt/docker-manager", nil
	}
	return "", errs.New(errs.COMPOSE_PROJECT_NOT_FOUND,
		"未找到宿主 docker-compose.yml。请设置 AGENT_COMPOSE_DIR 环境变量指向 compose 目录")
}

// ---------- docker client + helper 容器 ----------

// dockerClient 构造 moby client(DOCKER_HOST 优先,默认 unix socket)
func (s *State) dockerClient() (*client.Client, error) {
	if host := os.Getenv("DOCKER_HOST"); host != "" {
		return client.NewClientWithOpts(client.WithHost(host))
	}
	return client.NewClientWithOpts()
}

// runHelperContainer 创建并运行 helper 容器(替换 compose 镜像 tag 后 up)。
// helper 挂载 docker.sock + compose 目录,执行固定脚本(与 DockOrae 主程序机制一致)。
func runHelperContainer(cli *client.Client, ctx context.Context, composeDir, helperCmd, helperName string) error {
	opCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()

	pullRes, err := cli.ImagePull(opCtx, "docker/compose:latest", client.ImagePullOptions{})
	if err != nil {
		return errs.Newf(errs.COMPOSE_PULL_FAILED, "拉取 helper 镜像失败: %v", err)
	}
	for range pullRes.JSONMessages(opCtx) {
	}
	_ = pullRes.Close()

	_ = mustRemove(cli, opCtx, helperName)

	_, err = cli.ContainerCreate(opCtx, client.ContainerCreateOptions{
		Config: &container.Config{
			Image:      "docker/compose:latest",
			Cmd:        []string{"sh", "-c", helperCmd},
			WorkingDir: "/work",
		},
		HostConfig: &container.HostConfig{
			Binds: []string{
				"/var/run/docker.sock:/var/run/docker.sock",
				composeDir + ":/work",
			},
		},
		Name: helperName,
	})
	if err != nil {
		return errs.Newf(errs.COMPOSE_UPDATE_FAILED, "创建 helper 容器失败: %v", err)
	}
	if _, err := cli.ContainerStart(opCtx, helperName, client.ContainerStartOptions{}); err != nil {
		return errs.Newf(errs.COMPOSE_UPDATE_FAILED, "启动 helper 容器失败: %v", err)
	}
	// 等待退出并检查退出码
	waitCtx, waitCancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer waitCancel()
	wr := cli.ContainerWait(waitCtx, helperName, client.ContainerWaitOptions{Condition: container.WaitConditionNotRunning})
	select {
	case err := <-wr.Error:
		_ = mustRemove(cli, opCtx, helperName)
		return errs.Newf(errs.COMPOSE_UPDATE_FAILED, "等待 helper 退出失败: %v", err)
	case res := <-wr.Result:
		if res.StatusCode != 0 {
			detail := helperLogsTail(cli, helperName)
			_ = mustRemove(cli, opCtx, helperName)
			return errs.Newf(errs.COMPOSE_UPDATE_FAILED, "helper 执行失败(exit %d): %s", res.StatusCode, detail)
		}
	}
	_ = mustRemove(cli, opCtx, helperName)
	return nil
}

// helperLogsTail 读取 helper 容器最近日志(定位失败原因)
func helperLogsTail(cli *client.Client, id string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := cli.ContainerLogs(ctx, id, client.ContainerLogsOptions{ShowStdout: true, ShowStderr: true, Tail: "80"})
	if err != nil {
		return "(无日志输出)"
	}
	defer res.Close()
	buf := make([]byte, 0, 8192)
	tmp := make([]byte, 4096)
	for {
		n, err := res.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
		}
		if err != nil {
			break
		}
	}
	// 去掉 docker 日志流的 8 字节帧头(stdcopy)
	out := stripStdCopyFrames(buf)
	out = strings.TrimSpace(out)
	if out == "" {
		return "(无日志输出)"
	}
	lines := strings.Split(out, "\n")
	if len(lines) > 20 {
		lines = lines[len(lines)-20:]
	}
	return strings.Join(lines, "\n")
}

// stripStdCopyFrames 去掉 stdcopy 帧头
func stripStdCopyFrames(b []byte) string {
	out := make([]byte, 0, len(b))
	i := 0
	for i+8 <= len(b) {
		size := int(b[i+4])<<24 | int(b[i+5])<<16 | int(b[i+6])<<8 | int(b[i+7])
		if i+8+size > len(b) {
			break
		}
		out = append(out, b[i+8:i+8+size]...)
		i += 8 + size
	}
	return string(out)
}

var _ = fmt.Sprintf
var _ = sha256Hex
