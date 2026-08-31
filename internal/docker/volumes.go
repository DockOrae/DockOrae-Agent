// 卷操作(§10 Volume 全部由 Agent 执行)。
package docker

import (
	"context"
	"encoding/json"

	"github.com/moby/moby/api/types/volume"
	"github.com/moby/moby/client"

	"github.com/DockOrae/DockOrae-Agent/internal/errs"
)

// VolumeList 卷列表
func (s *Service) VolumeList(ctx context.Context) ([]volume.Volume, error) {
	cli, err := s.Client()
	if err != nil {
		return nil, dockerErr(err)
	}
	res, err := cli.VolumeList(ctx, client.VolumeListOptions{})
	if err != nil {
		return nil, dockerErr(err)
	}
	return res.Items, nil
}

// VolumeInspectRaw 卷详情原始 JSON
func (s *Service) VolumeInspectRaw(ctx context.Context, name string) (json.RawMessage, error) {
	cli, err := s.Client()
	if err != nil {
		return nil, dockerErr(err)
	}
	res, err := cli.VolumeInspect(ctx, name, client.VolumeInspectOptions{})
	if err != nil {
		return nil, dockerErr(err)
	}
	return res.Raw, nil
}

// VolumeCreate 创建卷(local 驱动;NFS 等经 driver_opts)
func (s *Service) VolumeCreate(ctx context.Context, req client.VolumeCreateOptions) (volume.Volume, error) {
	if req.Name == "" {
		return volume.Volume{}, errs.New(errs.INVALID_REQUEST, "name 不能为空")
	}
	cli, err := s.Client()
	if err != nil {
		return volume.Volume{}, dockerErr(err)
	}
	res, err := cli.VolumeCreate(ctx, req)
	if err != nil {
		return volume.Volume{}, dockerErr(err)
	}
	return res.Volume, nil
}

// VolumeRemove 删除卷
func (s *Service) VolumeRemove(ctx context.Context, name string, force bool) error {
	cli, err := s.Client()
	if err != nil {
		return dockerErr(err)
	}
	if _, err := cli.VolumeRemove(ctx, name, client.VolumeRemoveOptions{Force: force}); err != nil {
		return dockerErr(err)
	}
	return nil
}

// VolumePrune 清理未使用卷
func (s *Service) VolumePrune(ctx context.Context) (volume.PruneReport, error) {
	cli, err := s.Client()
	if err != nil {
		return volume.PruneReport{}, dockerErr(err)
	}
	res, err := cli.VolumePrune(ctx, client.VolumePruneOptions{})
	if err != nil {
		return volume.PruneReport{}, dockerErr(err)
	}
	return res.Report, nil
}
