// 镜像操作(§8 Image 全部由 Agent 执行)。
package docker

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/moby/moby/api/types/image"
	"github.com/moby/moby/client"

	"github.com/DockOrae/DockOrae-Agent/internal/errs"
)

// ImageList 镜像列表
func (s *Service) ImageList(ctx context.Context) ([]image.Summary, error) {
	cli, err := s.Client()
	if err != nil {
		return nil, dockerErr(err)
	}
	res, err := cli.ImageList(ctx, client.ImageListOptions{})
	if err != nil {
		return nil, dockerErr(err)
	}
	return res.Items, nil
}

// ImageInspectRaw 镜像详情原始 JSON(Inspector 结果无 Raw 字段,序列化返回)
func (s *Service) ImageInspectRaw(ctx context.Context, id string) (json.RawMessage, error) {
	cli, err := s.Client()
	if err != nil {
		return nil, dockerErr(err)
	}
	res, err := cli.ImageInspect(ctx, id)
	if err != nil {
		return nil, dockerErr(err)
	}
	return json.Marshal(res.InspectResponse)
}

// ImageRemove 删除镜像
func (s *Service) ImageRemove(ctx context.Context, id string, force bool) error {
	cli, err := s.Client()
	if err != nil {
		return dockerErr(err)
	}
	if _, err := cli.ImageRemove(ctx, id, client.ImageRemoveOptions{Force: force, PruneChildren: false}); err != nil {
		return dockerErr(err)
	}
	return nil
}

// ImagePrune 清理悬空镜像
func (s *Service) ImagePrune(ctx context.Context) (image.PruneReport, error) {
	cli, err := s.Client()
	if err != nil {
		return image.PruneReport{}, dockerErr(err)
	}
	res, err := cli.ImagePrune(ctx, client.ImagePruneOptions{})
	if err != nil {
		return image.PruneReport{}, dockerErr(err)
	}
	return res.Report, nil
}

// ImageTag 打标签
func (s *Service) ImageTag(ctx context.Context, id, repo, tag string) error {
	if repo == "" {
		return errs.New(errs.INVALID_REQUEST, "repo 不能为空")
	}
	if tag == "" {
		tag = "latest"
	}
	cli, err := s.Client()
	if err != nil {
		return dockerErr(err)
	}
	if _, err := cli.ImageTag(ctx, client.ImageTagOptions{Source: id, Target: strings.TrimSpace(repo) + ":" + strings.TrimSpace(tag)}); err != nil {
		return dockerErr(err)
	}
	return nil
}
