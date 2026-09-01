// Package docker Docker Engine 管理(§18-§19)。
// status/info/version 经 docker.sock(moby SDK);start/stop/restart 为 systemd docker 服务操作;
// cleanup 结构化清理:执行前返回预计释放空间与待删对象数量(§19),危险操作需确认。
package docker

import (
	"context"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/moby/moby/client"

	"github.com/DockOrae/DockOrae-Agent/internal/errs"
	"github.com/DockOrae/DockOrae-Agent/internal/hostexec"
)

// Service docker 服务
type Service struct {
	Exec *hostexec.Execer
	// execSlots 同容器并发 Exec 计数(上限 MaxExecConcurrency)
	execSlots *execSlotTracker
}

// New 构造
func New(e *hostexec.Execer) *Service {
	return &Service{Exec: e, execSlots: newExecSlotTracker()}
}

// Client 惰性构造 moby client(DOCKER_HOST 优先)
func (s *Service) Client() (*client.Client, error) {
	if host := os.Getenv("DOCKER_HOST"); host != "" {
		return client.NewClientWithOpts(client.WithHost(host))
	}
	return client.NewClientWithOpts()
}

// Status Docker 状态(daemon ping + systemd 服务状态)
func (s *Service) Status(ctx context.Context) (map[string]any, error) {
	out := map[string]any{}
	cli, err := s.Client()
	if err != nil {
		return nil, errs.Newf(errs.DOCKER_UNAVAILABLE, "构造 docker client 失败: %v", err)
	}
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if _, err := cli.Ping(pingCtx, client.PingOptions{}); err != nil {
		out["daemon"] = "unreachable"
		out["running"] = false
		out["error"] = err.Error()
	} else {
		out["daemon"] = "ok"
		out["running"] = true
	}
	// systemd 服务状态(尽力而为)
	if st, err := s.Exec.OutputString("systemctl", "is-active", "docker"); err == nil {
		out["systemd_service"] = st
	} else {
		out["systemd_service"] = "unknown"
	}
	return out, nil
}

// Info Docker info(SDK)
func (s *Service) Info(ctx context.Context) (map[string]any, error) {
	cli, err := s.Client()
	if err != nil {
		return nil, errs.Newf(errs.DOCKER_UNAVAILABLE, "构造 docker client 失败: %v", err)
	}
	res, err := cli.Info(ctx, client.InfoOptions{})
	if err != nil {
		return nil, errs.Newf(errs.DOCKER_UNAVAILABLE, "docker info 失败: %v", err)
	}
	info := res.Info
	return map[string]any{
		"server_version":     info.ServerVersion,
		"containers":         info.Containers,
		"containers_running": info.ContainersRunning,
		"containers_paused":  info.ContainersPaused,
		"containers_stopped": info.ContainersStopped,
		"images":             info.Images,
		"storage_driver":     info.Driver,
		"memory":             info.MemTotal,
		"n_cpu":              info.NCPU,
		"name":               info.Name,
		"kernel_version":     info.KernelVersion,
		"operating_system":   info.OperatingSystem,
		"os_type":            info.OSType,
		"architecture":       info.Architecture,
		"cgroup_driver":      info.CgroupDriver,
	}, nil
}

// Version Docker 版本(SDK)
func (s *Service) Version(ctx context.Context) (map[string]any, error) {
	cli, err := s.Client()
	if err != nil {
		return nil, errs.Newf(errs.DOCKER_UNAVAILABLE, "构造 docker client 失败: %v", err)
	}
	v, err := cli.ServerVersion(ctx, client.ServerVersionOptions{})
	if err != nil {
		return nil, errs.Newf(errs.DOCKER_UNAVAILABLE, "docker version 失败: %v", err)
	}
	components := []map[string]string{}
	for _, c := range v.Components {
		components = append(components, map[string]string{
			"name": c.Name, "version": c.Version,
		})
	}
	return map[string]any{
		"version":         v.Version,
		"api_version":     v.APIVersion,
		"min_api_version": v.MinAPIVersion,
		"os":              v.Os,
		"arch":            v.Arch,
		"components":      components,
	}, nil
}

// ServiceAction docker 引擎服务操作(§18 start/stop/restart;复用 systemd 机制)
func (s *Service) ServiceAction(action string) (map[string]any, error) {
	switch action {
	case "start", "stop", "restart":
	default:
		return nil, errs.Newf(errs.INVALID_REQUEST, "不支持的 docker 服务操作: %s", action)
	}
	if _, err := s.Exec.Output("systemctl", action, "docker"); err != nil {
		// 回退 service 命令(非 systemd 发行版)
		if err2 := s.Exec.RunScript("service docker " + action); err2 != nil {
			return nil, errs.Newf(errs.EXEC_FAILED, "docker %s 失败: %v", action, err)
		}
	}
	// Verify
	st, _ := s.Exec.OutputString("systemctl", "is-active", "docker")
	return map[string]any{"ok": true, "action": action, "state": st}, nil
}

// dfRow docker system df 行
type dfRow struct {
	Type        string `json:"Type"`
	TotalCount  int64  `json:"TotalCount"`
	Active      int64  `json:"Active"`
	Size        string `json:"Size"`
	Reclaimable string `json:"Reclaimable"`
}

// SystemDF 读取 docker system df(JSON 行);失败返回空
func (s *Service) SystemDF() []dfRow {
	out, err := s.Exec.Output("docker", "system", "df", "--format", "{{json .}}")
	if err != nil {
		return nil
	}
	var rows []dfRow
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var r dfRow
		if err := jsonUnmarshal([]byte(line), &r); err == nil {
			rows = append(rows, r)
		}
	}
	return rows
}

// danglingNetworks 未使用网络数量(docker network ls -f dangling=true)
func (s *Service) danglingNetworks() int {
	out, err := s.Exec.Output("docker", "network", "ls", "-f", "dangling=true", "--format", "{{.Name}}")
	if err != nil {
		return 0
	}
	count := 0
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if strings.TrimSpace(line) != "" {
			count++
		}
	}
	return count
}

// CleanupPreview 清理预览(§19:执行前返回预计释放空间 + 待删除对象数量)
func (s *Service) CleanupPreview(ctx context.Context) (map[string]any, error) {
	cli, err := s.Client()
	if err != nil {
		return nil, errs.Newf(errs.DOCKER_UNAVAILABLE, "构造 docker client 失败: %v", err)
	}
	// 容器:停止数量
	stopped := int64(0)
	if res, err := cli.ContainerList(ctx, client.ContainerListOptions{All: true}); err == nil {
		for _, c := range res.Items {
			if string(c.State) != "running" {
				stopped++
			}
		}
	}
	// 悬空镜像
	dangling := int64(0)
	if res, err := cli.ImageList(ctx, client.ImageListOptions{Filters: danglingFilter()}); err == nil {
		dangling = int64(len(res.Items))
	}
	// 未使用网络 + 释放空间(docker CLI)
	netCount := int64(s.danglingNetworks())
	df := s.SystemDF()
	byType := map[string]dfRow{}
	for _, r := range df {
		byType[r.Type] = r
	}
	buildCount := int64(0)
	if r, ok := byType["Build Cache"]; ok {
		buildCount = r.TotalCount
	}
	containersInfo := map[string]any{"count": stopped, "reclaimable": rowReclaimable(byType, "Containers")}
	imagesInfo := map[string]any{"count": dangling, "reclaimable": rowReclaimable(byType, "Images")}
	buildInfo := map[string]any{"count": buildCount, "reclaimable": rowReclaimable(byType, "Build Cache")}
	netInfo := map[string]any{"count": netCount, "reclaimable": "0B"}
	volInfo := map[string]any{"count": rowCount(byType, "Volumes"), "reclaimable": rowReclaimable(byType, "Volumes")}
	return map[string]any{
		"containers": containersInfo, "images": imagesInfo, "networks": netInfo,
		"build_cache": buildInfo, "volumes": volInfo,
		"total_reclaimable": sumReclaimable(byType),
		"total_objects":     stopped + dangling + netCount + buildCount,
	}, nil
}

// Cleanup 执行清理(§19:结构化目标 + confirm)
func (s *Service) Cleanup(ctx context.Context, targets []string) (map[string]any, error) {
	allowed := map[string]bool{"containers": true, "images": true, "networks": true, "build_cache": true, "volumes": true}
	for _, t := range targets {
		if !allowed[t] {
			return nil, errs.Newf(errs.INVALID_REQUEST, "不支持的清理目标: %s", t)
		}
	}
	cli, err := s.Client()
	if err != nil {
		return nil, errs.Newf(errs.DOCKER_UNAVAILABLE, "构造 docker client 失败: %v", err)
	}
	result := map[string]any{}
	if contains(targets, "containers") {
		if rep, err := cli.ContainerPrune(ctx, client.ContainerPruneOptions{Filters: client.Filters{}}); err == nil {
			result["containers_reclaimed"] = rep.Report.SpaceReclaimed
			result["containers_removed"] = len(rep.Report.ContainersDeleted)
		}
	}
	if contains(targets, "images") {
		if rep, err := cli.ImagePrune(ctx, client.ImagePruneOptions{Filters: danglingFilter()}); err == nil {
			result["dangling_images_reclaimed"] = rep.Report.SpaceReclaimed
			result["dangling_images_removed"] = len(rep.Report.ImagesDeleted)
		}
	}
	if contains(targets, "build_cache") {
		if rep, err := cli.BuildCachePrune(ctx, client.BuildCachePruneOptions{All: true}); err == nil {
			result["build_cache_reclaimed"] = rep.Report.SpaceReclaimed
			result["build_cache_removed"] = len(rep.Report.CachesDeleted)
		}
	}
	if contains(targets, "networks") {
		if rep, err := cli.NetworkPrune(ctx, client.NetworkPruneOptions{Filters: client.Filters{}}); err == nil {
			result["networks_removed"] = len(rep.Report.NetworksDeleted)
		}
	}
	if contains(targets, "volumes") {
		if rep, err := cli.VolumePrune(ctx, client.VolumePruneOptions{}); err == nil {
			result["volumes_reclaimed"] = rep.Report.SpaceReclaimed
			result["volumes_removed"] = len(rep.Report.VolumesDeleted)
		}
	}
	// 清理后再查询预览,确认实际效果
	after, _ := s.CleanupPreview(ctx)
	result["after"] = after
	return result, nil
}

// ---------- 小工具 ----------

func danglingFilter() client.Filters {
	return (client.Filters{}).Add("dangling", "true")
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func rowCount(by map[string]dfRow, typ string) int64 {
	if r, ok := by[typ]; ok {
		return r.TotalCount
	}
	return 0
}

func rowReclaimable(by map[string]dfRow, typ string) string {
	if r, ok := by[typ]; ok {
		return r.Reclaimable
	}
	return "0B"
}

func sumReclaimable(by map[string]dfRow) string {
	total := int64(0)
	for _, r := range by {
		total += parseHumanBytes(r.Reclaimable)
	}
	return humanBytes(total)
}

// parseHumanBytes 解析 docker 的 "5.2GB"/"0B" 格式
func parseHumanBytes(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	mult := int64(1)
	upper := strings.ToUpper(s)
	switch {
	case strings.HasSuffix(upper, "TIB"):
		mult, s = 1<<40, s[:len(s)-3]
	case strings.HasSuffix(upper, "GIB"):
		mult, s = 1<<30, s[:len(s)-3]
	case strings.HasSuffix(upper, "MIB"):
		mult, s = 1<<20, s[:len(s)-3]
	case strings.HasSuffix(upper, "KIB"):
		mult, s = 1<<10, s[:len(s)-3]
	case strings.HasSuffix(upper, "TB"):
		mult, s = 1e12, s[:len(s)-2]
	case strings.HasSuffix(upper, "GB"):
		mult, s = 1e9, s[:len(s)-2]
	case strings.HasSuffix(upper, "MB"):
		mult, s = 1e6, s[:len(s)-2]
	case strings.HasSuffix(upper, "KB"):
		mult, s = 1e3, s[:len(s)-2]
	case strings.HasSuffix(upper, "B"):
		s = s[:len(s)-1]
	default:
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0
	}
	return int64(v * float64(mult))
}

// humanBytes 人类可读字节(展示用)
func humanBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return strconv.FormatInt(b, 10) + "B"
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return strconv.FormatFloat(float64(b)/float64(div), 'f', 1, 64) + " " + string("KMGTPE"[exp]) + "iB"
}
