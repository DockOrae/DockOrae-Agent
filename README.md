# DockOrae-Agent

DockOrae 的 **宿主机控制平面(Linux Host Control Plane)**。负责经过严格授权的宿主机操作:

```
                    Browser
                       │
                       ▼
              DockOrae Frontend
                       │
                       │ HTTP/API
                       ▼
                  DockOrae
               Main Backend
                       │
                  Unix Socket
                       │
                       ▼
              DockOrae-Agent
             Host Control Plane
                       │
       ┌───────────────┼────────────────┐
       │               │                │
       ▼               ▼                ▼
     Linux          Docker           System
      Host          Engine           Services
```

## 定位

| 负责 | 不负责 |
|---|---|
| Swap 管理(create/resize/delete) | Web UI / Frontend |
| 系统信息 / Hostname / Reboot | 用户管理 |
| Systemd 服务管理 | License 管理 |
| 时区 / 时间同步 | 业务数据库 |
| 系统更新(apt/apk) | 页面渲染 |
| Docker 引擎状态与清理 | |
| Compose 更新与回滚(digest 追踪) | |
| 面板托管 Compose 执行(up/down/start/stop/logs) | |
| 容器/镜像/网络/卷管理(§7-§10) | |
| 自身 Binary 在线更新(自动回滚) | |
| Disk / Sysctl(白名单) / Network | |

## 安全模型

- **仅 Unix Socket** 监听(`/run/dockorae/agent.sock`),绝不监听 TCP
- **双重认证**:Socket 组权限(0660 dockorae 组)+ 每次请求 Bearer Token(面板自动生成共享)
- **结构化 API**:固定 handler + 严格参数校验,**不存在任意 Shell 执行接口**
- **操作锁**:Swap / Update / Compose / System / Docker 互斥,防止危险操作并发
- **审计日志**:危险操作记录 谁/何时/操作/目标/结果/是否回滚(JSONL)
- **统一错误码**:`SWAP_INVALID_SIZE`、`UPDATE_CHECKSUM_FAILED`、`COMPOSE_ROLLBACK_FAILED` 等

## API(Unix Socket,HTTP JSON)

`Authorization: Bearer <token>`,响应 `{"ok":true,"data":...}` / `{"ok":false,"error":{"code","message"}}`

```
GET  /v1/health
GET  /v1/host/info  GET /v1/host/hostname  POST /v1/host/hostname  POST /v1/host/reboot
GET  /v1/system/info  GET/POST /v1/system/timezone  GET /v1/system/time  POST /v1/system/time/sync
POST /v1/system/service {name,action}  GET /v1/system/update/check  POST /v1/system/update
GET  /v1/swap/status  POST /v1/swap/create|resize|delete {size_mb,confirm}
GET  /v1/docker/status|info|version  POST /v1/docker/service {action}  GET /v1/docker/cleanup/preview  POST /v1/docker/cleanup
GET  /v1/docker/containers|images|networks|volumes(list/create/prune)  POST /v1/docker/containers/{id}/start|stop|restart|kill|pause|unpause|rename
WS   /v1/docker/containers/{id}/logs|stats|terminal  WS /v1/docker/events
POST /v1/compose/managed/up|pull|run|start|stop|restart|down|build(面板托管栈)  WS /v1/compose/managed/logs
GET  /v1/compose/projects|status|check_update  POST /v1/compose/pull|update|rollback
GET  /v1/binary/status  POST /v1/binary/check_update|download|install|rollback
GET  /v1/disk/usage|devices|mounts
GET  /v1/sysctl/get  POST /v1/sysctl/set(白名单)
GET  /v1/network/interfaces|routes|dns
```

## 部署

### 方式一:Docker Compose(与 DockOrae 主程序同栈)

见 DockOrae 仓库 `docker-compose.yml` 中的 `dockorae-agent` 服务:
privileged + pid:host + `/run/dockorae` 共享卷(socket + token)+ `/var/run/docker.sock`。
宿主操作经 `nsenter -t 1 -m -u -i -n` 进入宿主命名空间执行。

### 方式二:二进制(systemd)

```bash
curl -fsSL https://raw.githubusercontent.com/DockOrae/DockOrae-Agent/main/install.sh | bash
# 重复执行自动检测已安装;--force 强制重装;--uninstall 卸载
```

## 开发

```bash
make          # 构建
make test     # go vet + go test
make cross    # 交叉编译全部 Linux 架构(dist/*.tar.gz + sha256)
```

## 更新

Agent 自带在线更新(binary 部署):
下载 → SHA256 校验 → 备份 `exe.old` → 原子替换 → systemd 健康检查 → 失败自动回滚。
容器部署:helper 容器替换镜像 tag 后 recreate。

## License

GPL-3.0
