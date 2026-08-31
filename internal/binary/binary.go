// Package binary DockOrae-Agent 自身在线更新(§24-§27)。
// 安全要求(§56):HTTPS + SHA256 + 临时文件 + Backup + Atomic Replace + Health Check + Rollback。
// 禁止 curl|sh 式更新。
//
// 两种部署模式的安装策略:
//   - binary(systemd 直接跑宿主):下载 → 校验 → 解压 → 备份 exe.old → 原子替换 → systemd-run 健康检查 → 失败自动回滚;
//   - compose(容器):经 helper 容器替换宿主 compose 中的 agent 镜像 tag → up --force-recreate(与 DockOrae 主程序同款机制)。
package binary

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"golang.org/x/mod/semver"

	"github.com/DockOrae/DockOrae-Agent/internal/config"
	"github.com/DockOrae/DockOrae-Agent/internal/errs"
	"github.com/DockOrae/DockOrae-Agent/internal/hostexec"
)

// Phase 更新阶段(§27)
type Phase string

const (
	PhaseIdle        Phase = "idle"
	PhaseChecking    Phase = "checking"
	PhaseDownloading Phase = "downloading"
	PhaseVerifying   Phase = "verifying"
	PhaseInstalling  Phase = "installing"
	PhaseRestarting  Phase = "restarting"
	PhaseHealthCheck Phase = "health_check"
	PhaseSuccess     Phase = "success"
	PhaseFailed      Phase = "failed"
	PhaseRollback    Phase = "rollback"
)

// Status 更新状态
type Status struct {
	Running    bool      `json:"running"`
	Phase      Phase     `json:"phase"`
	Percent    int       `json:"percent"`
	Version    string    `json:"version"` // 目标版本
	Mode       string    `json:"mode"`    // binary | compose
	Error      string    `json:"error"`
	Rollback   string    `json:"rollback"` // rollback=success / rollback=failed
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`
}

// State 更新状态机(内存态 + 持久化状态文件,重启后仍可查询)
type State struct {
	mu     sync.Mutex
	status Status
	cfg    *config.Config
	exec   *hostexec.Execer
	token  string
	// 更新锁定:由 apiserver 的 oplock 管理,这里只做状态机
	lastError error
	// 下载待安装的临时文件(download → install 衔接)
	pendingPkg string
	pendingSum string
}

// 常量
const (
	// UpdateRepo 更新来源仓库:DockOrae 主项目 release(资产 dockorae-agent-linux-*.tar.gz 随主项目发布)
	UpdateRepo = "DockOrae/DockOrae"
	// UpdateGitHubURL 更新检测 API(GitHub releases latest)
	UpdateGitHubURL = "https://api.github.com/repos/" + UpdateRepo + "/releases/latest"
	// BinaryName 二进制名(与发布资产内一致)
	BinaryName = "dockorae-agent"
	// ServiceName systemd 服务名
	ServiceName = "dockorae-agent"
	// maxExtractBytes 解压体积上限(防压缩包炸弹)
	maxExtractBytes = 512 << 20
	// stateFile 持久化更新状态文件(数据目录下)
	stateFile = "update-state.json"
	// healthResultFile 健康检查结果文件(systemd-run 脚本写入)
	healthResultFile = "health-result"
)

// NewState 构造更新状态机
func NewState(cfg *config.Config) *State {
	s := &State{cfg: cfg, exec: hostexec.New(cfg.InContainer, cfg.ComposeBin)}
	s.load()
	return s
}

// CurrentVersion 当前版本
func (s *State) CurrentVersion() string { return versionOf() }

// Info 当前状态副本
func (s *State) Info() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}

// setPhase 推进阶段
func (s *State) setPhase(p Phase) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status.Running = p != PhaseIdle && p != PhaseSuccess && p != PhaseFailed
	s.status.Phase = p
	s.status.Percent = phasePercent(p)
	if p == PhaseSuccess || p == PhaseFailed {
		s.status.FinishedAt = time.Now()
	}
	s.save()
}

func (s *State) setError(format string, args ...any) error {
	msg := fmt.Sprintf(format, args...)
	s.mu.Lock()
	s.status.Running = false
	s.status.Phase = PhaseFailed
	s.status.Error = msg
	s.status.FinishedAt = time.Now()
	s.lastError = errors.New(msg)
	s.save()
	s.mu.Unlock()
	log.Printf("binary-update: %s", msg)
	return errors.New(msg)
}

func phasePercent(p Phase) int {
	switch p {
	case PhaseChecking:
		return 5
	case PhaseDownloading:
		return 15
	case PhaseVerifying:
		return 40
	case PhaseInstalling:
		return 60
	case PhaseRestarting:
		return 85
	case PhaseHealthCheck:
		return 95
	case PhaseSuccess:
		return 100
	case PhaseRollback:
		return 90
	}
	return 0
}

// CheckUpdate 检测更新(结果缓存 10 分钟;失败不缓存)
func (s *State) CheckUpdate(ctx context.Context) (map[string]any, error) {
	cur := s.CurrentVersion()
	info := map[string]any{"current": cur}

	rel, err := s.fetchRelease(ctx)
	if err != nil {
		return info, err
	}
	info["latest"] = rel.TagName
	info["has_update"] = CompareVersions(cur, rel.TagName) < 0
	info["mode"] = s.deployMode()
	// 可用资产
	asset, sumAsset := findAssets(rel.TagName, rel.Assets)
	info["installable"] = asset != ""
	info["asset"] = asset
	info["checksum_asset"] = sumAsset
	return info, nil
}

// releaseMeta 远端 release 元数据
type releaseMeta struct {
	TagName string `json:"tag_name"`
	Draft   bool   `json:"draft"`
	Assets  []struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
	} `json:"assets"`
}

// fetchRelease 拉取 release 元数据(固定 GitHub DockOrae/DockOrae releases/latest)
func (s *State) fetchRelease(ctx context.Context) (*releaseMeta, error) {
	checkCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(checkCtx, http.MethodGet, UpdateGitHubURL, nil)
	if err != nil {
		return nil, errs.Newf(errs.UPDATE_DOWNLOAD_FAILED, "构造请求失败: %v", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "dockorae-agent/"+s.CurrentVersion())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, errs.Newf(errs.UPDATE_DOWNLOAD_FAILED, "检测更新失败: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, errs.Newf(errs.UPDATE_DOWNLOAD_FAILED, "检测更新失败: HTTP %d", resp.StatusCode)
	}
	var rel releaseMeta
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&rel); err != nil || rel.TagName == "" {
		return nil, errs.Newf(errs.UPDATE_DOWNLOAD_FAILED, "release 数据无效")
	}
	return &rel, nil
}

// findAssetURLs 在 release 资产中查找包与校验和文件的下载 URL
func findAssetURLs(pkg string, assets []struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}) (string, string) {
	var base, sumURL string
	for _, a := range assets {
		switch a.Name {
		case pkg:
			base = a.BrowserDownloadURL
		case pkg + ".sha256":
			sumURL = a.BrowserDownloadURL
		}
	}
	return base, sumURL
}

// Download 下载目标版本资产(返回本地临时文件路径 + 校验和资产路径;不安装)
func (s *State) Download(ctx context.Context, tag string) (string, string, error) {
	if !validReleaseTag(tag) {
		return "", "", errs.New(errs.INVALID_REQUEST, "版本号非法")
	}
	// 已有下载中的任务则拒绝
	s.mu.Lock()
	if s.status.Running {
		s.mu.Unlock()
		return "", "", errs.New(errs.OPERATION_IN_PROGRESS, "更新任务进行中")
	}
	s.mu.Unlock()
	s.setPhase(PhaseDownloading)
	arch := runtime.GOARCH
	if arch != "amd64" && arch != "arm64" {
		return "", "", errs.Newf(errs.UNSUPPORTED, "不支持的架构: %s", arch)
	}
	pkg := fmt.Sprintf("%s-linux-%s.tar.gz", BinaryName, arch)
	// 资产固定从项目 GitHub releases 下载(https://github.com/DockOrae/DockOrae/releases)
	base := fmt.Sprintf("https://github.com/%s/releases/download/%s/%s", UpdateRepo, tag, pkg)
	sumURL := base + ".sha256"

	client := &http.Client{Timeout: 300 * time.Second}
	resp, err := client.Get(base)
	if err != nil {
		return "", "", errs.Newf(errs.UPDATE_DOWNLOAD_FAILED, "下载失败: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", errs.Newf(errs.UPDATE_DOWNLOAD_FAILED, "下载失败: HTTP %d", resp.StatusCode)
	}
	tmp, err := os.CreateTemp(s.cfg.DataDir, "agent-update-*.tar.gz")
	if err != nil {
		return "", "", errs.Newf(errs.UPDATE_DOWNLOAD_FAILED, "创建临时文件失败: %v", err)
	}
	defer tmp.Close()
	_, err = io.Copy(tmp, resp.Body)
	if err != nil {
		os.Remove(tmp.Name())
		return "", "", errs.Newf(errs.UPDATE_DOWNLOAD_FAILED, "下载中断: %v", err)
	}
	sumTmp := tmp.Name() + ".sha256"
	// 校验文件下载失败(老 release 无该资产)不阻塞主流程
	if sumResp, err := client.Get(sumURL); err == nil {
		defer sumResp.Body.Close()
		if sumResp.StatusCode == http.StatusOK {
			if b, err := io.ReadAll(io.LimitReader(sumResp.Body, 4096)); err == nil {
				_ = os.WriteFile(sumTmp, b, 0o600)
			}
		}
	}
	s.mu.Lock()
	s.status.Version = tag
	s.pendingPkg = tmp.Name()
	s.pendingSum = sumTmp
	s.mu.Unlock()
	return tmp.Name(), sumTmp, nil
}

// Verify 校验下载文件 SHA256(§25)
func (s *State) Verify(file, sumFile string) error {
	s.setPhase(PhaseVerifying)
	if err := verifySHA256(file, sumFile); err != nil {
		os.Remove(file)
		os.Remove(sumFile)
		return errs.Newf(errs.UPDATE_CHECKSUM_FAILED, "%v", err)
	}
	return nil
}

// Install 安装(按部署模式)
func (s *State) Install(ctx context.Context, pkgFile, sumFile, tag string) error {
	s.setPhase(PhaseInstalling)
	s.mu.Lock()
	s.status.Version = tag
	s.status.Mode = s.deployMode()
	s.mu.Unlock()

	if s.deployMode() == "compose" {
		err := s.installCompose(ctx, tag)
		if err != nil {
			return err
		}
	} else {
		err := s.installBinary(ctx, pkgFile, tag)
		if err != nil {
			return err
		}
	}
	s.setPhase(PhaseSuccess)
	return nil
}

// installBinary 二进制模式:解压 → 备份 → 原子替换 → systemd 健康检查(自动回滚)
func (s *State) installBinary(ctx context.Context, pkgFile, tag string) error {
	// 解压新二进制
	newBin, err := extractBinary(pkgFile)
	if err != nil {
		return s.setError("解压更新包失败: %v", err)
	}
	defer os.Remove(newBin)

	exe, err := os.Readlink("/proc/self/exe")
	if err != nil || exe == "" {
		return s.setError("无法定位当前二进制(/proc/self/exe)")
	}
	staged := exe + ".new"
	if err := copyFile(newBin, staged); err != nil {
		return s.setError("暂存新二进制失败: %v", err)
	}
	defer os.Remove(staged)
	if err := os.Chmod(staged, 0o755); err != nil {
		return s.setError("设置权限失败: %v", err)
	}
	_ = os.Remove(exe + ".old")
	if err := os.Rename(exe, exe+".old"); err != nil {
		return s.setError("备份旧二进制失败: %v", err)
	}
	if err := os.Rename(staged, exe); err != nil {
		_ = os.Rename(exe+".old", exe)
		return s.setError("替换二进制失败: %v", err)
	}
	log.Printf("binary-update: 二进制已替换 %s(备份 %s.old),即将重启服务", exe, exe)

	// 注册 systemd 一次性健康检查(独立于本进程;重启后验证,失败自动回滚)
	s.setPhase(PhaseHealthCheck)
	s.scheduleHealthCheck(exe, tag)

	// 延迟重启服务
	s.setPhase(PhaseRestarting)
	go func() {
		time.Sleep(1500 * time.Millisecond)
		if err := exec.Command("systemctl", "restart", ServiceName).Run(); err != nil {
			log.Printf("binary-update: systemctl restart %s 失败: %v(可手动恢复 %s.old)", ServiceName, err, exe)
		}
	}()
	return nil
}

// installCompose 容器模式:helper 容器替换宿主 compose 的 agent 镜像 tag 并 recreate
func (s *State) installCompose(ctx context.Context, tag string) error {
	dir, err := s.findComposeDir(ctx)
	if err != nil {
		return s.setError("%v", err)
	}
	composeFile := filepath.Join(dir, "docker-compose.yml")
	yamlBytes, err := s.exec.ReadFile(composeFile)
	if err != nil {
		return s.setError("读取宿主 docker-compose.yml 失败: %v", err)
	}
	oldVal, ok := findManagerImage(string(yamlBytes))
	if !ok {
		return s.setError("docker-compose.yml 中未找到 dockorae-agent 镜像(image 字段),已中止更新")
	}
	newVal := retagImageValue(oldVal, tag)
	if !safeImageValue(oldVal) || !safeImageValue(newVal) {
		return s.setError("compose 镜像值包含非法字符,已中止更新: %q", oldVal)
	}
	// 复用 DockOrae 主程序同款 helper 机制:docker/compose 镜像代跑 sed + up
	cli, err := s.dockerClient()
	if err != nil {
		return s.setError("无法连接 docker daemon: %v", err)
	}
	helper := fmt.Sprintf(
		"cp /work/docker-compose.yml /work/docker-compose.yml.bak 2>/dev/null; "+
			"sed -i 's|^\\( *image: *\\)%s\\(.*\\)$|\\1%s\\2|' /work/docker-compose.yml; "+
			"docker compose -f /work/docker-compose.yml up -d --force-recreate --pull always",
		oldVal, newVal)
	if err := runHelperContainer(cli, ctx, dir, helper, "dockorae-agent-update-helper"); err != nil {
		return s.setError("%v", err)
	}
	log.Printf("binary-update: compose 模式更新完成,agent 容器已重建(tag=%s)", tag)
	return nil
}

// Rollback 手动回滚(§26):恢复 exe.old 并重启
func (s *State) Rollback() error {
	s.setPhase(PhaseRollback)
	exe, err := os.Readlink("/proc/self/exe")
	if err != nil || exe == "" {
		return errs.New(errs.UPDATE_ROLLBACK_FAILED, "无法定位当前二进制")
	}
	old := exe + ".old"
	if _, err := os.Stat(old); err != nil {
		return errs.Newf(errs.UPDATE_ROLLBACK_FAILED, "无可用备份(%s)", old)
	}
	if err := copyFile(old, exe); err != nil {
		return errs.Newf(errs.UPDATE_ROLLBACK_FAILED, "恢复旧二进制失败: %v", err)
	}
	_ = os.Chmod(exe, 0o755)
	s.mu.Lock()
	s.status.Rollback = "success"
	s.mu.Unlock()
	log.Printf("binary-update: 手动回滚完成,重启服务")
	go func() {
		time.Sleep(500 * time.Millisecond)
		_ = exec.Command("systemctl", "restart", ServiceName).Run()
	}()
	return nil
}

// HealthCheckResult 读取持久化健康检查结果(重启后 /v1/binary/status 返回)
func (s *State) HealthCheckResult() string {
	b, err := os.ReadFile(filepath.Join(s.cfg.DataDir, healthResultFile))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// Pending 返回待安装的下载文件(download 后未 install)
func (s *State) Pending() (pkg, sum string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pendingPkg, s.pendingSum
}

// ---------- 健康检查(systemd-run 一次性单元) ----------

// scheduleHealthCheck 注册 systemd 一次性健康检查单元
// 脚本:等待服务起来 → 执行 dockorae-agent -version 比对 → 不一致则回滚 exe.old 并重启服务。
func (s *State) scheduleHealthCheck(exe, expect string) {
	if _, err := exec.LookPath("systemd-run"); err != nil {
		log.Printf("binary-update: systemd-run 不可用,跳过自动健康检查/回滚(旧二进制保留于 %s.old)", exe)
		return
	}
	resultFile := filepath.Join(s.cfg.DataDir, healthResultFile)
	script := fmt.Sprintf(
		`sleep 8; ver=$(%s -version 2>/dev/null | grep -o 'v[0-9][0-9.]*' | head -1); `+
			`if [ -n "$ver" ] && [ "$ver" = "%s" ]; then echo "healthy %s" > %s; exit 0; fi; `+
			`echo "health failed (ver=$ver) rolling back" > %s; `+
			`cp -f %s.old %s; chmod +x %s; systemctl restart %s; exit 1`,
		exe, expect, expect, resultFile, resultFile, exe, exe, exe, ServiceName)
	unit := fmt.Sprintf("dockorae-agent-update-%d", time.Now().UnixNano()%1000000)
	cmd := exec.Command("systemd-run", "--on-active=10", "--unit="+unit, "--collect", "/bin/sh", "-c", script)
	if out, err := cmd.CombinedOutput(); err != nil {
		log.Printf("binary-update: 注册 systemd 健康检查失败: %v %s", err, strings.TrimSpace(string(out)))
	} else {
		log.Printf("binary-update: 已注册健康检查单元 %s", unit)
	}
}

// ---------- 状态持久化 ----------

func (s *State) save() {
	b, err := json.Marshal(s.status)
	if err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(s.cfg.DataDir, stateFile), b, 0o600)
}

func (s *State) load() {
	b, err := os.ReadFile(filepath.Join(s.cfg.DataDir, stateFile))
	if err != nil {
		return
	}
	var st Status
	if json.Unmarshal(b, &st) == nil {
		// 重启后保留上次结果;Running 状态不可能跨进程存活
		if st.Phase == PhaseDownloading || st.Phase == PhaseVerifying || st.Phase == PhaseInstalling ||
			st.Phase == PhaseRestarting || st.Phase == PhaseHealthCheck {
			st.Phase = PhaseIdle
			st.Running = false
		}
		s.status = st
	}
}

// deployMode 部署模式(容器内 = compose,否则 binary)
func (s *State) deployMode() string {
	if s.cfg.InContainer {
		return "compose"
	}
	return "binary"
}

// ---------- 工具函数 ----------

var releaseTagRe = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+$`)

func validReleaseTag(t string) bool { return releaseTagRe.MatchString(t) }

// CompareVersions 基于 SemVer 比较(空/无效按 v0.0.0)
func CompareVersions(a, b string) int {
	return semver.Compare(normalizeSemVer(a), normalizeSemVer(b))
}

func normalizeSemVer(s string) string {
	s = strings.TrimSpace(strings.TrimPrefix(s, "v"))
	if s == "" {
		return "v0.0.0"
	}
	v := "v" + s
	if semver.IsValid(v) {
		return v
	}
	return "v0.0.0"
}

// findAssets 查找当前架构资产(amd64/arm64)
func findAssets(tag string, assets []struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}) (asset, sum string) {
	arch := runtime.GOARCH
	prefix := fmt.Sprintf("%s-linux-%s.tar.gz", BinaryName, arch)
	sumName := prefix + ".sha256"
	for _, a := range assets {
		switch a.Name {
		case prefix:
			asset = a.BrowserDownloadURL
		case sumName:
			sum = a.BrowserDownloadURL
		}
	}
	return asset, sum
}

// verifySHA256 校验文件 SHA256 与校验文件一致;校验文件缺失(老 release)跳过并告警
func verifySHA256(file, sumFile string) error {
	expected := ""
	if b, err := os.ReadFile(sumFile); err == nil {
		fields := strings.Fields(string(b))
		if len(fields) == 0 {
			return errors.New("校验文件格式无效")
		}
		expected = fields[0]
	}
	if expected == "" {
		log.Printf("binary-update: 校验文件缺失,跳过 SHA256 校验")
		return nil
	}
	got, err := sha256File(file)
	if err != nil {
		return err
	}
	if !strings.EqualFold(got, expected) {
		return fmt.Errorf("SHA256 校验失败: 期望 %s,实际 %s", expected, got)
	}
	return nil
}

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := newSHA256()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}

// extractBinary 从 tar.gz 解压出 dockorae-agent 可执行文件到临时目录
func extractBinary(tarGz string) (string, error) {
	f, err := os.Open(tarGz)
	if err != nil {
		return "", err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return "", err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	var total int64
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
		if hdr.Typeflag != tar.TypeReg || !strings.HasSuffix(hdr.Name, BinaryName) {
			continue
		}
		total += hdr.Size
		if total > maxExtractBytes {
			return "", errors.New("更新包解压体积超过限制")
		}
		out, err := os.CreateTemp("", "agent-bin-*")
		if err != nil {
			return "", err
		}
		if _, err := io.Copy(out, tr); err != nil {
			out.Close()
			os.Remove(out.Name())
			return "", err
		}
		out.Close()
		return out.Name(), nil
	}
	return "", fmt.Errorf("压缩包中未找到 %s", BinaryName)
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(dst)
		return err
	}
	return out.Close()
}

// versionOf 当前版本(cmd.Version 注入)
func versionOf() string {
	return cmdVersion()
}
