// 容器操作(§7 Container 全部由 Agent 执行)。
// 本文件为 moby SDK 直连实现,供 apiserver 的 /v1/docker/containers* 端点调用;
// DockOrae 主程序绝不直接接触 Docker Engine,仅经本 Agent 执行。
package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"strconv"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"

	"github.com/DockOrae/DockOrae-Agent/internal/errs"
)

// ContainerCreateReq 创建容器请求(config/host_config 为 docker API 原始 JSON,由面板透传)
type ContainerCreateReq struct {
	Name             string          `json:"name"`
	Config           json.RawMessage `json:"config"`
	HostConfig       json.RawMessage `json:"host_config"`
	NetworkingConfig json.RawMessage `json:"networking_config"`
}

// ContainerList 容器列表(全部精简字段,过滤由面板业务层完成)
func (s *Service) ContainerList(ctx context.Context, all bool) ([]container.Summary, error) {
	cli, err := s.Client()
	if err != nil {
		return nil, dockerErr(err)
	}
	res, err := cli.ContainerList(ctx, client.ContainerListOptions{All: all})
	if err != nil {
		return nil, dockerErr(err)
	}
	return res.Items, nil
}

// ContainerInspectRaw 容器详情原始 JSON(面板透传前端,并用于重建容器)
func (s *Service) ContainerInspectRaw(ctx context.Context, id string) (json.RawMessage, error) {
	cli, err := s.Client()
	if err != nil {
		return nil, dockerErr(err)
	}
	res, err := cli.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
	if err != nil {
		return nil, dockerErr(err)
	}
	return res.Raw, nil
}

// ContainerCreate 创建容器(config/host_config 为原始 JSON,经严格解码后交给 SDK)
func (s *Service) ContainerCreate(ctx context.Context, req ContainerCreateReq) (string, error) {
	if len(req.Config) == 0 {
		return "", errs.New(errs.INVALID_REQUEST, "config 不能为空")
	}
	cli, err := s.Client()
	if err != nil {
		return "", dockerErr(err)
	}
	opts := client.ContainerCreateOptions{Name: req.Name}
	if err := json.Unmarshal(req.Config, &opts.Config); err != nil {
		return "", errs.Newf(errs.INVALID_REQUEST, "config 无效: %v", err)
	}
	if len(req.HostConfig) > 0 {
		if err := json.Unmarshal(req.HostConfig, &opts.HostConfig); err != nil {
			return "", errs.Newf(errs.INVALID_REQUEST, "host_config 无效: %v", err)
		}
	}
	if len(req.NetworkingConfig) > 0 {
		var nc network.NetworkingConfig
		if err := json.Unmarshal(req.NetworkingConfig, &nc); err != nil {
			return "", errs.Newf(errs.INVALID_REQUEST, "networking_config 无效: %v", err)
		}
		opts.NetworkingConfig = &nc
	}
	res, err := cli.ContainerCreate(ctx, opts)
	if err != nil {
		return "", dockerErr(err)
	}
	return res.ID, nil
}

// ContainerStart 启动容器
func (s *Service) ContainerStart(ctx context.Context, id string) error {
	cli, err := s.Client()
	if err != nil {
		return dockerErr(err)
	}
	if _, err := cli.ContainerStart(ctx, id, client.ContainerStartOptions{}); err != nil {
		return dockerErr(err)
	}
	return nil
}

// ContainerStop 停止容器
func (s *Service) ContainerStop(ctx context.Context, id string, timeout *int) error {
	cli, err := s.Client()
	if err != nil {
		return dockerErr(err)
	}
	opts := client.ContainerStopOptions{}
	if timeout != nil {
		opts.Timeout = timeout
	}
	if _, err := cli.ContainerStop(ctx, id, opts); err != nil {
		return dockerErr(err)
	}
	return nil
}

// ContainerRestart 重启容器
func (s *Service) ContainerRestart(ctx context.Context, id string, timeout *int) error {
	cli, err := s.Client()
	if err != nil {
		return dockerErr(err)
	}
	opts := client.ContainerRestartOptions{}
	if timeout != nil {
		opts.Timeout = timeout
	}
	if _, err := cli.ContainerRestart(ctx, id, opts); err != nil {
		return dockerErr(err)
	}
	return nil
}

// ContainerKill 强制终止容器
func (s *Service) ContainerKill(ctx context.Context, id string) error {
	cli, err := s.Client()
	if err != nil {
		return dockerErr(err)
	}
	if _, err := cli.ContainerKill(ctx, id, client.ContainerKillOptions{Signal: "SIGKILL"}); err != nil {
		return dockerErr(err)
	}
	return nil
}

// ContainerPause 暂停容器
func (s *Service) ContainerPause(ctx context.Context, id string) error {
	cli, err := s.Client()
	if err != nil {
		return dockerErr(err)
	}
	if _, err := cli.ContainerPause(ctx, id, client.ContainerPauseOptions{}); err != nil {
		return dockerErr(err)
	}
	return nil
}

// ContainerUnpause 恢复暂停
func (s *Service) ContainerUnpause(ctx context.Context, id string) error {
	cli, err := s.Client()
	if err != nil {
		return dockerErr(err)
	}
	if _, err := cli.ContainerUnpause(ctx, id, client.ContainerUnpauseOptions{}); err != nil {
		return dockerErr(err)
	}
	return nil
}

// ContainerRename 重命名容器
func (s *Service) ContainerRename(ctx context.Context, id, newName string) error {
	if newName == "" {
		return errs.New(errs.INVALID_REQUEST, "name 不能为空")
	}
	cli, err := s.Client()
	if err != nil {
		return dockerErr(err)
	}
	if _, err := cli.ContainerRename(ctx, id, client.ContainerRenameOptions{NewName: newName}); err != nil {
		return dockerErr(err)
	}
	return nil
}

// ContainerRemove 删除容器
func (s *Service) ContainerRemove(ctx context.Context, id string, force, removeVolumes bool) error {
	cli, err := s.Client()
	if err != nil {
		return dockerErr(err)
	}
	if _, err := cli.ContainerRemove(ctx, id, client.ContainerRemoveOptions{Force: force, RemoveVolumes: removeVolumes}); err != nil {
		return dockerErr(err)
	}
	return nil
}

// ContainerPrune 清理已停止容器
func (s *Service) ContainerPrune(ctx context.Context) (container.PruneReport, error) {
	cli, err := s.Client()
	if err != nil {
		return container.PruneReport{}, dockerErr(err)
	}
	res, err := cli.ContainerPrune(ctx, client.ContainerPruneOptions{})
	if err != nil {
		return container.PruneReport{}, dockerErr(err)
	}
	return res.Report, nil
}

// ContainerWait 等待容器退出,返回退出码(面板在线更新 helper 用)
func (s *Service) ContainerWait(ctx context.Context, id string) (int64, error) {
	cli, err := s.Client()
	if err != nil {
		return 0, dockerErr(err)
	}
	res := cli.ContainerWait(ctx, id, client.ContainerWaitOptions{Condition: container.WaitConditionNotRunning})
	select {
	case r := <-res.Result:
		return r.StatusCode, nil
	case err := <-res.Error:
		return 0, dockerErr(err)
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}

// ContainerLogsTail 容器日志尾部文本(stdcopy 解复用;更新失败诊断用)
func (s *Service) ContainerLogsTail(ctx context.Context, id string, lines int) (string, error) {
	cli, err := s.Client()
	if err != nil {
		return "", dockerErr(err)
	}
	logs, err := cli.ContainerLogs(ctx, id, client.ContainerLogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Tail:       strconv.Itoa(lines),
	})
	if err != nil {
		return "", dockerErr(err)
	}
	defer logs.Close()
	var buf bytes.Buffer
	_, _ = stdcopy.StdCopy(&buf, &buf, logs)
	return buf.String(), nil
}

// dockerErr moby 错误 → Agent 统一错误
func dockerErr(err error) error {
	if err == nil {
		return nil
	}
	if ae, ok := err.(*errs.Error); ok {
		return ae
	}
	return errs.Newf(errs.DOCKER_ERROR, "%v", err)
}
