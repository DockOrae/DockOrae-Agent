// 网络操作(§9 Docker Network 全部由 Agent 执行)。
package docker

import (
	"context"
	"encoding/json"

	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"

	"github.com/DockOrae/DockOrae-Agent/internal/errs"
)

// NetworkList 网络列表
func (s *Service) NetworkList(ctx context.Context) ([]network.Summary, error) {
	cli, err := s.Client()
	if err != nil {
		return nil, dockerErr(err)
	}
	res, err := cli.NetworkList(ctx, client.NetworkListOptions{})
	if err != nil {
		return nil, dockerErr(err)
	}
	return res.Items, nil
}

// NetworkInspectRaw 网络详情原始 JSON
func (s *Service) NetworkInspectRaw(ctx context.Context, id string) (json.RawMessage, error) {
	cli, err := s.Client()
	if err != nil {
		return nil, dockerErr(err)
	}
	res, err := cli.NetworkInspect(ctx, id, client.NetworkInspectOptions{})
	if err != nil {
		return nil, dockerErr(err)
	}
	return res.Raw, nil
}

// NetworkCreateReq 创建网络请求(IPAM 为可选的原始 JSON)
type NetworkCreateReq struct {
	Name     string          `json:"name"`
	Driver   string          `json:"driver"`
	Internal bool            `json:"internal"`
	IPAM     json.RawMessage `json:"ipam"` // network.IPAM 原始 JSON(子网/网关)
}

// NetworkCreate 创建网络
func (s *Service) NetworkCreate(ctx context.Context, req NetworkCreateReq) (string, error) {
	if req.Name == "" {
		return "", errs.New(errs.INVALID_REQUEST, "name 不能为空")
	}
	driver := req.Driver
	if driver == "" {
		driver = "bridge"
	}
	cli, err := s.Client()
	if err != nil {
		return "", dockerErr(err)
	}
	opts := client.NetworkCreateOptions{
		Driver:   driver,
		Internal: req.Internal,
	}
	if len(req.IPAM) > 0 {
		var ipam network.IPAM
		if err := json.Unmarshal(req.IPAM, &ipam); err != nil {
			return "", errs.Newf(errs.INVALID_REQUEST, "ipam 无效: %v", err)
		}
		opts.IPAM = &ipam
	}
	res, err := cli.NetworkCreate(ctx, req.Name, opts)
	if err != nil {
		return "", dockerErr(err)
	}
	return res.ID, nil
}

// NetworkRemove 删除网络
func (s *Service) NetworkRemove(ctx context.Context, id string) error {
	cli, err := s.Client()
	if err != nil {
		return dockerErr(err)
	}
	if _, err := cli.NetworkRemove(ctx, id, client.NetworkRemoveOptions{}); err != nil {
		return dockerErr(err)
	}
	return nil
}

// NetworkPrune 清理未使用网络
func (s *Service) NetworkPrune(ctx context.Context) (network.PruneReport, error) {
	cli, err := s.Client()
	if err != nil {
		return network.PruneReport{}, dockerErr(err)
	}
	res, err := cli.NetworkPrune(ctx, client.NetworkPruneOptions{})
	if err != nil {
		return network.PruneReport{}, dockerErr(err)
	}
	return res.Report, nil
}

// NetworkConnect 容器接入网络
func (s *Service) NetworkConnect(ctx context.Context, id, container string) error {
	if container == "" {
		return errs.New(errs.INVALID_REQUEST, "container 不能为空")
	}
	cli, err := s.Client()
	if err != nil {
		return dockerErr(err)
	}
	if _, err := cli.NetworkConnect(ctx, id, client.NetworkConnectOptions{Container: container}); err != nil {
		return dockerErr(err)
	}
	return nil
}

// NetworkDisconnect 容器断开网络
func (s *Service) NetworkDisconnect(ctx context.Context, id, container string, force bool) error {
	if container == "" {
		return errs.New(errs.INVALID_REQUEST, "container 不能为空")
	}
	cli, err := s.Client()
	if err != nil {
		return dockerErr(err)
	}
	if _, err := cli.NetworkDisconnect(ctx, id, client.NetworkDisconnectOptions{Container: container, Force: force}); err != nil {
		return dockerErr(err)
	}
	return nil
}
