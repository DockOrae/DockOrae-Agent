#!/usr/bin/env bash
# ============================================================
# DockOrae-Agent 安装脚本(二进制部署, systemd)
# 用法:
#   curl -fsSL https://raw.githubusercontent.com/DockOrae/DockOrae-Agent/main/install.sh | bash
#   bash install.sh                安装/升级
#   bash install.sh --force        强制重装
#   bash install.sh --uninstall    卸载
#
# 特性:
#   - 重复执行自动检测已安装版本与服务状态(不重复安装)
#   - 自动检测国内/海外网络,国内走 GitHub 加速站
#   - 与 DockOrae 主程序共享 /run/dockorae(socket + token)
# ============================================================
set -euo pipefail

# ---------- 配置 ----------
VERSION="${VERSION:-latest}"
INSTALL_DIR="${AGENT_INSTALL_DIR:-/opt/dockorae-agent}"
DATA_DIR="${AGENT_DATA_DIR:-/var/lib/dockorae-agent}"
LOG_DIR="${AGENT_LOG_DIR:-/var/log/dockorae}"
SERVICE_NAME="dockorae-agent"
BIN_PATH="/usr/local/bin/dockorae-agent"
GITHUB_REPO="DockOrae/DockOrae-Agent"
# 国内加速站(网络检测后使用)
GH_PROXY="https://ghfast.top"
# 归档内二进制相对路径
PKG_SUBDIR="dockorae-agent"

# ---------- 颜色 ----------
GREEN='\033[0;32m'; RED='\033[0;31m'; YELLOW='\033[1;33m'; NC='\033[0m'
info()  { echo -e "${GREEN}[INFO]${NC} $*"; }
warn()  { echo -e "${YELLOW}[WARN]${NC} $*"; }
error() { echo -e "${RED}[ERROR]${NC} $*"; }
die()   { error "$*"; exit 1; }

# ---------- 帮助 ----------
usage() {
  echo "用法: bash install.sh [--force] [--uninstall]"
  echo "  --force        强制重装(已安装时覆盖)"
  echo "  --uninstall    卸载 Agent"
  exit 0
}

# ---------- root 检查 ----------
[ "$(id -u)" -eq 0 ] || die "请以 root 运行"

# ---------- 参数解析 ----------
FORCE=0
for arg in "$@"; do
  case "$arg" in
    --force) FORCE=1 ;;
    --uninstall) UNINSTALL=1 ;;
    -h|--help) usage ;;
  esac
done

# ---------- 架构检测 ----------
case "$(uname -m)" in
  x86_64)  ARCH="amd64" ;;
  aarch64) ARCH="arm64" ;;
  armv7l|armv6l) ARCH="arm" ;;
  *) die "不支持的架构: $(uname -m)" ;;
esac

# ---------- 网络检测(国内/海外分流) ----------
detect_network() {
  if curl -fsSL --max-time 5 -o /dev/null "https://api.github.com" 2>/dev/null; then
    echo "direct"
  else
    echo "china"
  fi
}
NET="$(detect_network)"
[ "$NET" = "china" ] && info "检测到国内网络,使用 GitHub 加速站" || info "网络正常,直连 GitHub"

download_url() {
  local ver="$1" base="https://github.com/${GITHUB_REPO}/releases/download"
  if [ "$NET" = "china" ]; then
    echo "${GH_PROXY}/${base}/${ver}/dockorae-agent-linux-${ARCH}.tar.gz"
  else
    echo "${base}/${ver}/dockorae-agent-linux-${ARCH}.tar.gz"
  fi
}

# ---------- 已安装检测(重复执行友好) ----------
if [ -x "$BIN_PATH" ] && [ "${UNINSTALL:-0}" -eq 0 ] && [ "$FORCE" -eq 0 ]; then
  CUR="$("$BIN_PATH" -version 2>/dev/null | awk '{print $2}' || echo "unknown")"
  if systemctl is-active "$SERVICE_NAME" >/dev/null 2>&1; then
    info "DockOrae-Agent 已安装(版本 ${CUR})且服务运行中,跳过安装"
    info "如需强制重装: bash install.sh --force"
  else
    warn "DockOrae-Agent 已安装(版本 ${CUR})但服务未运行,尝试启动..."
    systemctl start "$SERVICE_NAME" 2>/dev/null && info "服务已启动" || warn "服务启动失败,可执行 bash install.sh --force 重装"
  fi
  exit 0
fi

# ---------- 卸载 ----------
if [ "${UNINSTALL:-0}" -eq 1 ]; then
  info "卸载 DockOrae-Agent..."
  systemctl stop "$SERVICE_NAME" 2>/dev/null || true
  systemctl disable "$SERVICE_NAME" 2>/dev/null || true
  rm -f "/etc/systemd/system/${SERVICE_NAME}.service" "$BIN_PATH"
  systemctl daemon-reload
  info "已卸载(数据目录 ${DATA_DIR} 保留)"
  exit 0
fi

# ---------- 下载 ----------
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
PKG="dockorae-agent-linux-${ARCH}.tar.gz"
URL="$(download_url "$VERSION")"
info "下载 ${URL}"
curl -fL --retry 3 --connect-timeout 15 -o "$TMP/$PKG" "$URL" || die "下载失败"

# SHA256 校验(存在校验文件时)
SUM_URL="${URL}.sha256"
if SUM="$(curl -fsSL --max-time 10 "$SUM_URL" 2>/dev/null)" && [ -n "$SUM" ]; then
  EXPECT="$(echo "$SUM" | awk '{print $1}')"
  GOT="$(sha256sum "$TMP/$PKG" | awk '{print $1}')"
  [ "$EXPECT" = "$GOT" ] || die "SHA256 校验失败: 期望 ${EXPECT},实际 ${GOT}"
  info "SHA256 校验通过"
fi

# ---------- 安装 ----------
mkdir -p "$INSTALL_DIR" "$DATA_DIR" "$LOG_DIR"
tar -xzf "$TMP/$PKG" -C "$TMP"
BIN_SRC="$TMP/${PKG_SUBDIR}/dockorae-agent"
[ -x "$BIN_SRC" ] || die "安装包内未找到 dockorae-agent 二进制"
cp -f "$BIN_SRC" "$BIN_PATH"
chmod +x "$BIN_PATH"

# 共享目录 /run/dockorae(与主程序共用 socket + token)
mkdir -p /run/dockorae

# systemd 服务
cat > "/etc/systemd/system/${SERVICE_NAME}.service" <<EOF
[Unit]
Description=DockOrae Agent (Host Control Plane)
After=network.target docker.service
Wants=network.target

[Service]
Type=simple
ExecStart=${BIN_PATH}
Restart=always
RestartSec=3
Environment=AGENT_SOCKET=/run/dockorae/agent.sock
Environment=AGENT_DATA_DIR=${DATA_DIR}
Environment=AGENT_LOG_DIR=${LOG_DIR}
Environment=AGENT_TOKEN_FILE=/run/dockorae/agent.token
# 容器内才需要 nsenter;二进制部署直连宿主
Environment=AGENT_HOST=1

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable "$SERVICE_NAME" >/dev/null 2>&1
systemctl restart "$SERVICE_NAME"

sleep 1
if systemctl is-active "$SERVICE_NAME" >/dev/null 2>&1; then
  VER="$("$BIN_PATH" -version 2>/dev/null | awk '{print $2}')"
  info "✅ DockOrae-Agent 安装成功(版本 ${VER})"
  info "   Socket: /run/dockorae/agent.sock"
  info "   服务:   systemctl status ${SERVICE_NAME}"
  info "   日志:   journalctl -u ${SERVICE_NAME} -f"
else
  error "服务启动失败,查看日志: journalctl -u ${SERVICE_NAME} -n 50"
  exit 1
fi
