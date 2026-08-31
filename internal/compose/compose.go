// Package compose 宿主 Docker Compose 管理(§20-§23)。
// 安全模型:compose 命令经宿主 docker CLI 执行(容器模式经 nsenter 进入宿主命名空间),
// 项目名/路径经严格校验,绝不接受任意路径或命令。
// 更新流程(§21):Check → Detect New Image → Pull → Record Current State → Update → Start → Health Check → Success。
// 回滚(§23):更新前记录 Previous Image/Digest/Compose 状态;失败自动回滚。
package compose

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/DockOrae/DockOrae-Agent/internal/config"
	"github.com/DockOrae/DockOrae-Agent/internal/errs"
	"github.com/DockOrae/DockOrae-Agent/internal/hostexec"
)

// projectNameRe 项目名校验:字母数字 _ - .
var projectNameRe = regexp.MustCompile(`^[a-zA-Z0-9_.-]+$`)

// pathRe 绝对路径校验
var pathRe = regexp.MustCompile(`^/([a-zA-Z0-9._-]+/)*[a-zA-Z0-9._-]+$`)

// Service compose 服务
type Service struct {
	Exec *hostexec.Execer
	Cfg  *config.Config

	mu       sync.Mutex
	state    map[string]*ProjectState // project → 状态(内存 + 持久化)
	stateVer int
}

// ProjectState 项目更新历史(§55:保存 Current + Previous)
type ProjectState struct {
	Project string         `json:"project"`
	Updates []UpdateRecord `json:"updates"` // 最新在前
}

// UpdateRecord 单次更新记录
type UpdateRecord struct {
	UpdateID    string          `json:"update_id"`
	Time        string          `json:"time"`
	Status      string          `json:"status"` // success / failed / rolled_back
	Rollback    string          `json:"rollback"`
	ComposeHash string          `json:"compose_sha256"`
	Services    []ServiceRecord `json:"services"` // 更新后 digest
	Previous    []ServiceRecord `json:"previous"` // 更新前 digest
}

// ServiceRecord 服务镜像 digest 记录(§22)
type ServiceRecord struct {
	Service string `json:"service"`
	Image   string `json:"image"`  // 如 nginx:1.25
	Digest  string `json:"digest"` // 如 sha256:xxx
}

// ComposeProject compose 项目摘要
type ComposeProject struct {
	Name     string `json:"name"`
	Status   string `json:"status"`
	Configs  string `json:"config_files"`
	Dir      string `json:"project_directory"`
	Services int    `json:"services"`
}

// New 构造 compose 服务
func New(e *hostexec.Execer, cfg *config.Config) *Service {
	s := &Service{Exec: e, Cfg: cfg, state: make(map[string]*ProjectState)}
	s.loadState()
	return s
}

// ValidateProject 校验项目名
func ValidateProject(p string) (string, error) {
	if p == "" || len(p) > 64 || strings.HasPrefix(p, "-") || !projectNameRe.MatchString(p) {
		return "", errs.New(errs.INVALID_REQUEST, "compose 项目名非法")
	}
	return p, nil
}

// ValidatePath 校验路径(仅用于已确认的宿主 compose 文件路径)
func ValidatePath(p string) (string, error) {
	if p == "" || len(p) > 512 || !pathRe.MatchString(p) {
		return "", errs.New(errs.INVALID_REQUEST, "路径非法")
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." || seg == "." {
			return "", errs.New(errs.INVALID_REQUEST, "路径不允许包含 .. 或 . 段")
		}
	}
	return filepath.Clean(p), nil
}

// composeCmd 构造 compose 命令(project 过滤)
func (s *Service) composeCmd(project string, args ...string) *execCmd {
	all := args
	if project != "" {
		all = append([]string{"-p", project}, all...)
	}
	return &execCmd{exec: s.Exec, args: all}
}

// execCmd compose 命令封装
type execCmd struct {
	exec *hostexec.Execer
	args []string
}

// run 执行(默认 5 分钟超时)
func (c *execCmd) run(timeout time.Duration) ([]byte, error) {
	return c.exec.ComposeOutput(timeout, c.args...)
}

// Projects 列出宿主全部 compose 项目
func (s *Service) Projects() ([]ComposeProject, error) {
	out, err := s.Exec.ComposeOutput(30*time.Second, "ls", "-a", "--format", "json")
	if err != nil {
		// 兼容旧版 compose:无 --format 支持时降级
		return nil, errs.Newf(errs.COMPOSE_PROJECT_NOT_FOUND, "列出 compose 项目失败: %v", err)
	}
	var raw []struct {
		Name         string `json:"Name"`
		Status       string `json:"Status"`
		ConfigFiles  string `json:"ConfigFiles"`
		ProjectDir   string `json:"ProjectDirectory"`
		ServiceCount int    `json:"ServiceCount"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, errs.Newf(errs.COMPOSE_PROJECT_NOT_FOUND, "解析 compose ls 失败: %v", err)
	}
	projects := make([]ComposeProject, 0, len(raw))
	for _, p := range raw {
		projects = append(projects, ComposeProject{
			Name: p.Name, Status: p.Status, Configs: p.ConfigFiles, Dir: p.ProjectDir, Services: p.ServiceCount,
		})
	}
	return projects, nil
}

// resolveProject 解析项目:按名字匹配;找不到返回 COMPOSE_PROJECT_NOT_FOUND
func (s *Service) resolveProject(name string) (ComposeProject, error) {
	projects, err := s.Projects()
	if err != nil {
		return ComposeProject{}, err
	}
	for _, p := range projects {
		if p.Name == name {
			return p, nil
		}
	}
	return ComposeProject{}, errs.Newf(errs.COMPOSE_PROJECT_NOT_FOUND, "compose 项目 %q 不存在", name)
}

// Status 项目状态(容器 + 镜像 digest)
func (s *Service) Status(project string) (map[string]any, error) {
	name, err := ValidateProject(project)
	if err != nil {
		return nil, err
	}
	p, err := s.resolveProject(name)
	if err != nil {
		return nil, err
	}
	// 容器状态
	psOut, err := s.Exec.ComposeOutput(30*time.Second, "-p", name, "-f", p.Configs, "ps", "--format", "json")
	if err != nil {
		return nil, errs.Newf(errs.EXEC_FAILED, "compose ps 失败: %v", err)
	}
	// 镜像 digest
	imgOut, err := s.Exec.ComposeOutput(30*time.Second, "-p", name, "-f", p.Configs, "images", "--format", "json")
	if err != nil {
		imgOut = nil // 镜像信息尽力而为
	}
	containers := parseComposePS(psOut)
	var images []ServiceRecord
	if len(imgOut) > 0 {
		if recs, err := parseComposeImages(imgOut); err == nil {
			images = recs
		}
	}
	return map[string]any{
		"name":       p.Name,
		"status":     p.Status,
		"dir":        p.Dir,
		"config":     p.Configs,
		"containers": containers,
		"images":     images,
	}, nil
}

// CheckUpdate 检查更新:拉取镜像(不应用)并对比 digest(§20-§22)
func (s *Service) CheckUpdate(project string) (map[string]any, error) {
	name, err := ValidateProject(project)
	if err != nil {
		return nil, err
	}
	p, err := s.resolveProject(name)
	if err != nil {
		return nil, err
	}
	before, _ := s.serviceDigests(name, p.Configs)
	// pull(静默;只拉不改动运行中容器)
	pullOut, err := s.Exec.ComposeOutput(10*time.Minute, "-p", name, "-f", p.Configs, "pull", "--quiet")
	if err != nil {
		return nil, errs.Newf(errs.COMPOSE_PULL_FAILED, "拉取镜像失败: %v", err)
	}
	_ = pullOut
	after, _ := s.serviceDigests(name, p.Configs)
	// 对比:digest 变化的服务 = 有更新
	updates := []map[string]any{}
	for _, a := range after {
		var old string
		for _, b := range before {
			if b.Service == a.Service {
				old = b.Digest
				break
			}
		}
		if old != "" && a.Digest != "" && old != a.Digest {
			updates = append(updates, map[string]any{
				"service": a.Service, "image": a.Image, "from_digest": old, "to_digest": a.Digest,
			})
		}
	}
	return map[string]any{
		"project": name, "has_update": len(updates) > 0, "updates": updates,
		"images": after,
	}, nil
}

// Pull 仅拉取镜像(幂等,不影响运行中容器)
func (s *Service) Pull(project string) (map[string]any, error) {
	name, err := ValidateProject(project)
	if err != nil {
		return nil, err
	}
	p, err := s.resolveProject(name)
	if err != nil {
		return nil, err
	}
	if _, err := s.Exec.ComposeOutput(10*time.Minute, "-p", name, "-f", p.Configs, "pull", "--quiet"); err != nil {
		return nil, errs.Newf(errs.COMPOSE_PULL_FAILED, "拉取镜像失败: %v", err)
	}
	return map[string]any{"ok": true, "project": name}, nil
}

// Update 更新项目(§21 全流程;失败自动回滚 §23)
func (s *Service) Update(project string) (map[string]any, error) {
	name, err := ValidateProject(project)
	if err != nil {
		return nil, err
	}
	p, err := s.resolveProject(name)
	if err != nil {
		return nil, err
	}
	// 1. Record current state(更新前)
	before, _ := s.serviceDigests(name, p.Configs)
	composeHash := fileHash(p.Configs)
	updateID := fmt.Sprintf("%s-%d", name, time.Now().Unix())

	// 2. Pull + 应用
	if _, err := s.Exec.ComposeOutput(10*time.Minute, "-p", name, "-f", p.Configs, "pull", "--quiet"); err != nil {
		return nil, errs.Newf(errs.COMPOSE_PULL_FAILED, "拉取镜像失败: %v", err)
	}
	upOut, upErr := s.Exec.ComposeOutput(10*time.Minute, "-p", name, "-f", p.Configs, "up", "-d", "--remove-orphans")
	if upErr != nil {
		// 3. 自动回滚
		rbErr := s.rollbackDigests(name, p.Configs, before)
		s.record(name, updateID, composeHash, before, nil, "failed", rbErr)
		msg := fmt.Sprintf("compose 更新失败: %v", upErr)
		if rbErr != nil {
			msg += fmt.Sprintf(";自动回滚失败: %v", rbErr)
		}
		return nil, errs.Newf(errs.COMPOSE_UPDATE_FAILED, "%s", msg)
	}
	_ = upOut

	// 4. Health Check(§21:Start → Health Check)
	if err := s.healthCheck(name, p.Configs); err != nil {
		rbErr := s.rollbackDigests(name, p.Configs, before)
		s.record(name, updateID, composeHash, before, nil, "failed", rbErr)
		msg := fmt.Sprintf("compose 健康检查失败: %v", err)
		if rbErr != nil {
			msg += fmt.Sprintf(";自动回滚失败: %v", rbErr)
		}
		return nil, errs.Newf(errs.COMPOSE_HEALTHCHECK_FAILED, "%s", msg)
	}

	// 5. 记录成功状态
	after, _ := s.serviceDigests(name, p.Configs)
	rec := s.record(name, updateID, composeHash, before, after, "success", nil)
	return map[string]any{
		"ok": true, "project": name, "update_id": updateID,
		"previous": before, "images": after, "history": rec,
	}, nil
}

// Rollback 手动回滚到上一次记录的 digest(§23)
func (s *Service) Rollback(project string) (map[string]any, error) {
	name, err := ValidateProject(project)
	if err != nil {
		return nil, err
	}
	p, err := s.resolveProject(name)
	if err != nil {
		return nil, err
	}
	rec := s.lastRecord(name)
	if rec == nil || len(rec.Previous) == 0 {
		return nil, errs.Newf(errs.COMPOSE_ROLLBACK_FAILED, "项目 %q 无可用回滚记录", name)
	}
	if err := s.rollbackDigests(name, p.Configs, rec.Previous); err != nil {
		return nil, errs.Newf(errs.COMPOSE_ROLLBACK_FAILED, "回滚失败: %v", err)
	}
	// 健康检查
	if err := s.healthCheck(name, p.Configs); err != nil {
		return nil, errs.Newf(errs.COMPOSE_ROLLBACK_FAILED, "回滚后健康检查失败: %v", err)
	}
	// 标记记录为已回滚
	s.mu.Lock()
	if st, ok := s.state[name]; ok && len(st.Updates) > 0 {
		st.Updates[0].Status = "rolled_back"
		st.Updates[0].Rollback = "success"
		s.saveState()
	}
	s.mu.Unlock()
	return map[string]any{"ok": true, "project": name, "rolled_back": true}, nil
}

// History 项目更新历史
func (s *Service) History(project string) ([]UpdateRecord, error) {
	name, err := ValidateProject(project)
	if err != nil {
		return nil, err
	}
	rec := s.lastRecord(name)
	if rec == nil {
		return []UpdateRecord{}, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.state[name]
	if st == nil {
		return []UpdateRecord{}, nil
	}
	return st.Updates, nil
}

// ---------- 内部 ----------

// serviceDigests 读取各服务当前镜像 digest。
// 兼容新旧两种 compose 输出格式:
//   - 新版(v2.30+ / v5.x):ContainerName/Repository/Tag/ID
//   - 旧版:Service/Image/Digest
//
// ID 即 sha256 镜像 ID,可反映内容更新。
func (s *Service) serviceDigests(project, configs string) ([]ServiceRecord, error) {
	out, err := s.Exec.ComposeOutput(30*time.Second, "-p", project, "-f", configs, "images", "--format", "json")
	if err != nil {
		return nil, err
	}
	return parseComposeImages(out)
}

// parseComposeImages 解析 compose images --format json 输出,兼容新旧两种字段形态。
// 新版字段优先(ContainerName/Repository/Tag/ID),旧版字段(Service/Image/Digest)回退。
func parseComposeImages(out []byte) ([]ServiceRecord, error) {
	var raw []struct {
		Service       string `json:"Service"`
		Image         string `json:"Image"`
		Digest        string `json:"Digest"`
		ID            string `json:"ID"`
		ContainerName string `json:"ContainerName"`
		Repository    string `json:"Repository"`
		Tag           string `json:"Tag"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, err
	}
	recs := make([]ServiceRecord, 0, len(raw))
	for _, r := range raw {
		svc := r.Service
		if svc == "" {
			svc = r.ContainerName
		}
		img := r.Image
		if img == "" {
			img = r.Repository
			if r.Tag != "" {
				img += ":" + r.Tag
			}
		}
		dg := r.Digest
		if dg == "" {
			dg = r.ID
		}
		recs = append(recs, ServiceRecord{Service: svc, Image: img, Digest: dg})
	}
	return recs, nil
}

// healthCheck 基本健康检查:等待所有服务容器进入 running 状态(最多 60 秒)。
// 命中 running 后再隔 3 秒复查一次,防"启动即退出"竞态(如 busybox exit 1)。
func (s *Service) healthCheck(project, configs string) error {
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		out, err := s.Exec.ComposeOutput(15*time.Second, "-p", project, "-f", configs, "ps", "--format", "json")
		if err == nil {
			containers := parseComposePS(out)
			if len(containers) > 0 {
				allUp := true
				for _, c := range containers {
					st, _ := c["State"].(string)
					if !strings.EqualFold(st, "running") {
						allUp = false
						break
					}
				}
				if allUp {
					// 稳定性复查:3 秒后容器仍在运行才算健康
					time.Sleep(3 * time.Second)
					if out2, err2 := s.Exec.ComposeOutput(15*time.Second, "-p", project, "-f", configs, "ps", "--format", "json"); err2 == nil {
						cs2 := parseComposePS(out2)
						stillUp := len(cs2) > 0
						for _, c := range cs2 {
							st, _ := c["State"].(string)
							if !strings.EqualFold(st, "running") {
								stillUp = false
								break
							}
						}
						if stillUp {
							return nil
						}
					}
					// 复查未通过:继续轮询直至超时
				}
			}
		}
		time.Sleep(3 * time.Second)
	}
	return fmt.Errorf("容器未在 60 秒内稳定进入 running 状态")
}

// parseComposePS 解析 compose ps --format json 输出。
// 兼容单对象/多行/数组三种形态(docker compose v5.5 单容器输出单对象)。
func parseComposePS(out []byte) []map[string]any {
	var result []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var m map[string]any
		if json.Unmarshal([]byte(line), &m) == nil {
			result = append(result, m)
			continue
		}
		var arr []map[string]any
		if json.Unmarshal([]byte(line), &arr) == nil {
			result = append(result, arr...)
		}
	}
	return result
}

// rollbackDigests 回滚:把本地镜像重新 tag 到 compose 中引用的 tag,再 up(不拉取)。
// 若旧记录缺失 digest,则跳过该服务(无法回滚的保持当前)。
func (s *Service) rollbackDigests(project, configs string, previous []ServiceRecord) error {
	cmds := []string{}
	for _, rec := range previous {
		if rec.Digest == "" {
			continue
		}
		// 从 image 中拆出 repository 与 tag:"nginx:1.27-alpine" → repo=nginx, tag=1.27-alpine;
		// docker tag 源只能用 repo@digest(不允许 tag@digest 混合引用)
		img := rec.Image
		tag := ""
		if i := strings.LastIndex(img, ":"); i > strings.LastIndex(img, "/") {
			tag = img[i+1:]
			img = img[:i]
		}
		cmds = append(cmds, fmt.Sprintf("docker tag %s@%s %s:%s", img, rec.Digest, img, tag))
	}
	if len(cmds) == 0 {
		return fmt.Errorf("无可用回滚 digest")
	}
	script := strings.Join(cmds, " && ") + fmt.Sprintf(" && %s -p %s -f %s up -d --pull never --remove-orphans",
		s.composeBinStr(), hostexec.Quote(project), hostexec.Quote(configs))
	if err := s.Exec.RunScript(script); err != nil {
		return err
	}
	return nil
}

// composeBinStr compose 命令字符串(docker compose / docker-compose)
func (s *Service) composeBinStr() string {
	bin := s.Exec.ComposeBin
	if len(bin) == 0 {
		return "docker compose"
	}
	return strings.Join(bin, " ")
}

// record 记录更新(最新在前,保留 5 条)
func (s *Service) record(project, updateID, composeHash string, prev, after []ServiceRecord, status string, rbErr error) *UpdateRecord {
	rec := &UpdateRecord{
		UpdateID: updateID, Time: time.Now().Format(time.RFC3339), Status: status,
		ComposeHash: composeHash, Previous: prev, Services: after,
	}
	if rbErr != nil {
		rec.Rollback = "failed"
	} else if status == "failed" {
		rec.Rollback = "success"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.state[project]
	if st == nil {
		st = &ProjectState{Project: project}
		s.state[project] = st
	}
	st.Updates = append([]UpdateRecord{*rec}, st.Updates...)
	if len(st.Updates) > 5 {
		st.Updates = st.Updates[:5]
	}
	s.saveState()
	return rec
}

// lastRecord 最近一条更新记录
func (s *Service) lastRecord(project string) *UpdateRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.state[project]
	if st == nil || len(st.Updates) == 0 {
		return nil
	}
	r := st.Updates[0]
	return &r
}

// ---------- 持久化 ----------

func (s *Service) stateFile() string { return filepath.Join(s.Cfg.DataDir, "compose-state.json") }

func (s *Service) saveState() {
	b, err := json.Marshal(s.state)
	if err != nil {
		return
	}
	_ = os.WriteFile(s.stateFile(), b, 0o600)
}

func (s *Service) loadState() {
	b, err := os.ReadFile(s.stateFile())
	if err != nil {
		return
	}
	_ = json.Unmarshal(b, &s.state)
	if s.state == nil {
		s.state = make(map[string]*ProjectState)
	}
}

// fileHash 文件 SHA256
func fileHash(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
