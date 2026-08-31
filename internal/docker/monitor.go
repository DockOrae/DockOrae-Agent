// 容器网络/磁盘 IO 速率采样(面板监控每 3 秒轮询,此处 8 秒缓存差分)
// 与镜像加速(daemon.json registry-mirrors)读写:宿主 Docker 配置归 Agent 管理。
package docker

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"

	"github.com/DockOrae/DockOrae-Agent/internal/errs"
)

// ---------- 容器 IO 速率 ----------

var (
	ioCacheMu   sync.Mutex
	ioCacheAt   time.Time
	ioCachePrev time.Time
	ioCacheRx   uint64
	ioCacheTx   uint64
	ioCacheRd   uint64
	ioCacheWr   uint64
	ioRateRx    float64
	ioRateTx    float64
	ioRateRd    float64
	ioRateWr    float64
)

const ioCacheTTL = 8 * time.Second

// ContainerIO 所有运行中容器的网络收发/磁盘读写速率(B/s)与最近采样累计值
func (s *Service) ContainerIO(ctx context.Context) map[string]any {
	ioCacheMu.Lock()
	defer ioCacheMu.Unlock()
	if !ioCacheAt.IsZero() && time.Since(ioCacheAt) < ioCacheTTL {
		return ioResult(ioRateRx, ioRateTx, ioRateRd, ioRateWr, ioCacheRx, ioCacheTx, ioCacheRd, ioCacheWr)
	}
	curRx, curTx, curRd, curWr, ok := s.sampleContainerIO(ctx)
	now := time.Now()
	if ok && !ioCachePrev.IsZero() {
		if dt := now.Sub(ioCachePrev).Seconds(); dt > 0 {
			ioRateRx = rateDelta(curRx, ioCacheRx, dt)
			ioRateTx = rateDelta(curTx, ioCacheTx, dt)
			ioRateRd = rateDelta(curRd, ioCacheRd, dt)
			ioRateWr = rateDelta(curWr, ioCacheWr, dt)
		}
	}
	if ok {
		ioCacheRx, ioCacheTx, ioCacheRd, ioCacheWr = curRx, curTx, curRd, curWr
		ioCachePrev = now
	} else {
		curRx, curTx, curRd, curWr = ioCacheRx, ioCacheTx, ioCacheRd, ioCacheWr
	}
	ioCacheAt = now
	return ioResult(ioRateRx, ioRateTx, ioRateRd, ioRateWr, curRx, curTx, curRd, curWr)
}

func ioResult(rxRate, txRate, rdRate, wrRate float64, curRx, curTx, curRd, curWr uint64) map[string]any {
	return map[string]any{
		"net": map[string]any{"rx_rate": rxRate, "tx_rate": txRate, "rx_total": curRx, "tx_total": curTx},
		"io":  map[string]any{"read_rate": rdRate, "write_rate": wrRate, "read_total": curRd, "write_total": curWr},
	}
}

func rateDelta(cur, prev uint64, dt float64) float64 {
	if cur < prev || dt <= 0 {
		return 0
	}
	return float64(cur-prev) / dt
}

// sampleContainerIO 采样所有运行中容器的网络收发与磁盘读写累计值
func (s *Service) sampleContainerIO(ctx context.Context) (rx, tx, rd, wr uint64, ok bool) {
	cli, err := s.Client()
	if err != nil {
		return 0, 0, 0, 0, false
	}
	res, err := cli.ContainerList(ctx, client.ContainerListOptions{})
	if err != nil {
		return 0, 0, 0, 0, false
	}
	for _, c := range res.Items {
		if string(c.State) != "running" {
			continue
		}
		statsRes, err := cli.ContainerStats(ctx, c.ID, client.ContainerStatsOptions{})
		if err != nil {
			continue
		}
		var st container.StatsResponse
		decErr := json.NewDecoder(statsRes.Body).Decode(&st)
		statsRes.Body.Close()
		if decErr != nil {
			continue
		}
		for _, n := range st.Networks {
			rx += n.RxBytes
			tx += n.TxBytes
		}
		for _, r := range st.BlkioStats.IoServiceBytesRecursive {
			switch r.Op {
			case "read":
				rd += r.Value
			case "write":
				wr += r.Value
			}
		}
	}
	return rx, tx, rd, wr, true
}

// ---------- 镜像加速 (daemon.json registry-mirrors) ----------

// DaemonJSONPath 宿主 daemon.json 路径
func (s *Service) DaemonJSONPath() string {
	for _, p := range []string{"/etc/docker/daemon.json"} {
		if s.Exec.Exists(p) {
			return p
		}
	}
	return "/etc/docker/daemon.json"
}

// RegistryMirrors 读取当前镜像加速配置
func (s *Service) RegistryMirrors() (map[string]any, error) {
	path := s.DaemonJSONPath()
	mirrors := []string{}
	exists := s.Exec.Exists(path)
	if exists {
		raw, err := s.Exec.ReadFileString(path)
		if err == nil && strings.TrimSpace(raw) != "" {
			var v map[string]any
			if json.Unmarshal([]byte(raw), &v) == nil {
				if arr, ok := v["registry-mirrors"].([]any); ok {
					for _, m := range arr {
						if str, ok := m.(string); ok {
							mirrors = append(mirrors, str)
						}
					}
				}
			}
		}
	}
	return map[string]any{"mirrors": mirrors, "path": path, "exists": exists}, nil
}

// SaveRegistryMirrors 写入镜像加速配置(宿主 daemon.json)
func (s *Service) SaveRegistryMirrors(mirrors []string) error {
	path := s.DaemonJSONPath()
	cfg := map[string]any{}
	if raw, err := s.Exec.ReadFileString(path); err == nil && strings.TrimSpace(raw) != "" {
		if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
			return errs.Newf(errs.VALIDATION_ERROR, "daemon.json 解析失败: %v", err)
		}
	}
	if len(mirrors) == 0 {
		delete(cfg, "registry-mirrors")
	} else {
		cfg["registry-mirrors"] = mirrors
	}
	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return errs.Newf(errs.VALIDATION_ERROR, "序列化失败: %v", err)
	}
	if err := s.Exec.WriteFile(path, out, 0o644); err != nil {
		return errs.Newf(errs.HOST_ERROR, "写入 daemon.json 失败: %v", err)
	}
	return nil
}
