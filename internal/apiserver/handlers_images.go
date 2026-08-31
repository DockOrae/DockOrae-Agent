// 镜像 REST 端点(§8 Image 全部由 Agent 执行)。
package apiserver

import (
	"context"
	"encoding/json"
	"io"
	"net/http"

	"github.com/moby/moby/client"

	"github.com/DockOrae/DockOrae-Agent/internal/errs"
	"github.com/DockOrae/DockOrae-Agent/internal/oplock"
)

// handleImagesList 镜像列表
func (s *Server) handleImagesList(c *Ctx) error {
	items, err := s.Docker.ImageList(c.R.Context())
	if err != nil {
		return err
	}
	c.OK(map[string]any{"items": items})
	return nil
}

// handleImageInspect 镜像详情(原始 JSON)
func (s *Server) handleImageInspect(c *Ctx) error {
	raw, err := s.Docker.ImageInspectRaw(c.R.Context(), c.Param("id"))
	if err != nil {
		return err
	}
	c.W.Header().Set("Content-Type", "application/json")
	_, _ = c.W.Write(append([]byte(`{"ok":true,"data":`), append(raw, []byte(`}`)...)...))
	return nil
}

// handleImagePull 拉取镜像(NDJSON 流式进度;经 moby ImagePull 逐条转发)
func (s *Server) handleImagePull(c *Ctx) error {
	var req struct {
		Image string `json:"image"` // 完整引用,如 nginx:1.25
	}
	if err := c.Bind(&req); err != nil {
		return err
	}
	if req.Image == "" {
		return errs.New(errs.INVALID_REQUEST, "image 不能为空")
	}
	cli, err := s.Docker.Client()
	if err != nil {
		return errs.Newf(errs.DOCKER_UNAVAILABLE, "构造 docker client 失败: %v", err)
	}
	pullRes, err := cli.ImagePull(c.R.Context(), req.Image, client.ImagePullOptions{})
	if err != nil {
		return errs.Newf(errs.DOCKER_ERROR, "拉取镜像失败: %v", err)
	}
	defer pullRes.Close()

	c.W.Header().Set("Content-Type", "application/x-ndjson")
	c.W.WriteHeader(http.StatusOK)
	flusher, _ := c.W.(interface{ Flush() })
	enc := json.NewEncoder(c.W)
	dec := json.NewDecoder(pullRes)
	start := now()
	for {
		var msg map[string]any
		if err := dec.Decode(&msg); err != nil {
			if err == io.EOF {
				break
			}
			_ = enc.Encode(map[string]any{"error": err.Error()})
			break
		}
		if err := enc.Encode(msg); err != nil {
			break
		}
		if flusher != nil {
			flusher.Flush()
		}
	}
	c.Audit("image.pull", req.Image, start, nil, "", nil)
	return nil
}

// handleImageRemove 删除镜像(?force=1)
func (s *Server) handleImageRemove(c *Ctx) error {
	id := c.Param("id")
	force := c.Query("force") == "1" || c.Query("force") == "true"
	if err := s.Locks.Acquire(oplock.LockDocker, c.RequestID); err != nil {
		return err
	}
	defer s.Locks.Release(oplock.LockDocker)
	start := now()
	err := s.Docker.ImageRemove(c.R.Context(), id, force)
	c.Audit("image.remove", id, start, err, "", nil)
	return c.okOrErr(err)
}

// handleImagePrune 清理悬空镜像(危险操作需确认)
func (s *Server) handleImagePrune(c *Ctx) error {
	var req struct {
		Confirm *bool `json:"confirm"`
	}
	if err := c.Bind(&req); err != nil {
		return err
	}
	if err := c.Confirm(req.Confirm, "images.prune"); err != nil {
		return err
	}
	report, err := s.Docker.ImagePrune(c.R.Context())
	if err != nil {
		return err
	}
	c.OK(map[string]any{"images_deleted": report.ImagesDeleted, "space_reclaimed": report.SpaceReclaimed})
	return nil
}

// handleImageTag 打标签
func (s *Server) handleImageTag(c *Ctx) error {
	var req struct {
		Repo string `json:"repo"`
		Tag  string `json:"tag"`
	}
	if err := c.Bind(&req); err != nil {
		return err
	}
	start := now()
	err := s.Docker.ImageTag(c.R.Context(), c.Param("id"), req.Repo, req.Tag)
	c.Audit("image.tag", c.Param("id")+" → "+req.Repo+":"+req.Tag, start, err, "", nil)
	return c.okOrErr(err)
}

var _ = context.Background
