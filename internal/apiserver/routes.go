package apiserver

// 路由注册(按 Skill §3 模块划分)。
// 注意:全部为结构化 API,不存在任何任意命令/Shell 执行接口(§6)。
func (s *Server) registerRoutes() {
	// ---------- 基础 ----------
	s.register("GET", "/v1/health", s.handleHealth)
	s.register("GET", "/v1/version", s.handleVersion)

	// ---------- host ----------
	s.register("GET", "/v1/host/info", s.handleHostInfo)
	s.register("GET", "/v1/host/hostname", s.handleHostHostname)
	s.register("POST", "/v1/host/hostname", s.handleHostSetHostname)
	s.register("POST", "/v1/host/reboot", s.handleHostReboot)
	s.register("GET", "/v1/host/monitor", s.handleHostMonitor)

	// ---------- system ----------
	s.register("GET", "/v1/system/info", s.handleSystemInfo)
	s.register("GET", "/v1/system/timezone", s.handleSystemTimezone)
	s.register("POST", "/v1/system/timezone", s.handleSystemSetTimezone)
	s.register("GET", "/v1/system/time", s.handleSystemTime)
	s.register("POST", "/v1/system/time/sync", s.handleSystemTimeSync)
	s.register("POST", "/v1/system/service", s.handleSystemService)
	s.register("GET", "/v1/system/update/check", s.handleSystemUpdateCheck)
	s.register("POST", "/v1/system/update", s.handleSystemUpdate)

	// ---------- swap ----------
	s.register("GET", "/v1/swap/status", s.handleSwapStatus)
	s.register("POST", "/v1/swap/create", s.handleSwapCreate)
	s.register("POST", "/v1/swap/resize", s.handleSwapResize)
	s.register("POST", "/v1/swap/delete", s.handleSwapDelete)

	// ---------- docker ----------
	s.register("GET", "/v1/docker/status", s.handleDockerStatus)
	s.register("GET", "/v1/docker/info", s.handleDockerInfo)
	s.register("GET", "/v1/docker/version", s.handleDockerVersion)
	s.register("POST", "/v1/docker/service", s.handleDockerService) // start/stop/restart
	s.register("GET", "/v1/docker/cleanup/preview", s.handleDockerCleanupPreview)
	s.register("POST", "/v1/docker/cleanup", s.handleDockerCleanup)
	s.register("GET", "/v1/docker/io", s.handleDockerIO)
	s.register("GET", "/v1/docker/registry_mirrors", s.handleRegistryMirrorsGet)
	s.register("POST", "/v1/docker/registry_mirrors", s.handleRegistryMirrorsSave)

	// ---------- containers(§7) ----------
	s.register("GET", "/v1/docker/containers", s.handleContainersList)
	s.register("POST", "/v1/docker/containers", s.handleContainerCreate)
	s.register("POST", "/v1/docker/containers/prune", s.handleContainersPrune)
	s.register("GET", "/v1/docker/containers/{id}", s.handleContainerInspect)
	s.register("DELETE", "/v1/docker/containers/{id}", s.handleContainerRemove)
	s.register("POST", "/v1/docker/containers/{id}/start", s.handleContainerStart)
	s.register("POST", "/v1/docker/containers/{id}/stop", s.handleContainerStop)
	s.register("POST", "/v1/docker/containers/{id}/restart", s.handleContainerRestart)
	s.register("POST", "/v1/docker/containers/{id}/kill", s.handleContainerKill)
	s.register("POST", "/v1/docker/containers/{id}/pause", s.handleContainerPause)
	s.register("POST", "/v1/docker/containers/{id}/unpause", s.handleContainerUnpause)
	s.register("POST", "/v1/docker/containers/{id}/rename", s.handleContainerRename)
	s.register("GET", "/v1/docker/containers/{id}/wait", s.handleContainerWait)
	s.register("GET", "/v1/docker/containers/{id}/logs_tail", s.handleContainerLogsTail)
	s.register("POST", "/v1/docker/containers/{id}/exec", s.handleContainerExec)

	// ---------- images(§8) ----------
	s.register("GET", "/v1/docker/images", s.handleImagesList)
	s.register("POST", "/v1/docker/images/pull", s.handleImagePull)
	s.register("POST", "/v1/docker/images/prune", s.handleImagePrune)
	s.register("GET", "/v1/docker/images/{id}", s.handleImageInspect)
	s.register("DELETE", "/v1/docker/images/{id}", s.handleImageRemove)
	s.register("POST", "/v1/docker/images/{id}/tag", s.handleImageTag)

	// ---------- networks(§9) ----------
	s.register("GET", "/v1/docker/networks", s.handleNetworksList)
	s.register("POST", "/v1/docker/networks", s.handleNetworkCreate)
	s.register("POST", "/v1/docker/networks/prune", s.handleNetworksPrune)
	s.register("GET", "/v1/docker/networks/{id}", s.handleNetworkInspect)
	s.register("DELETE", "/v1/docker/networks/{id}", s.handleNetworkRemove)
	s.register("POST", "/v1/docker/networks/{id}/connect", s.handleNetworkConnect)
	s.register("POST", "/v1/docker/networks/{id}/disconnect", s.handleNetworkDisconnect)

	// ---------- volumes(§10) ----------
	s.register("GET", "/v1/docker/volumes", s.handleVolumesList)
	s.register("POST", "/v1/docker/volumes", s.handleVolumeCreate)
	s.register("POST", "/v1/docker/volumes/prune", s.handleVolumesPrune)
	s.register("GET", "/v1/docker/volumes/{name}", s.handleVolumeInspect)
	s.register("DELETE", "/v1/docker/volumes/{name}", s.handleVolumeRemove)

	// ---------- 面板托管 compose 执行(§11:面板管 YAML,Agent 执行) ----------
	s.register("POST", "/v1/compose/managed/up", s.handleComposeManagedUp)
	s.register("POST", "/v1/compose/managed/pull", s.handleComposeManagedPull)
	s.register("POST", "/v1/compose/managed/run", s.handleComposeManagedRun)
	s.register("POST", "/v1/compose/managed/start", s.handleComposeManagedStart)
	s.register("POST", "/v1/compose/managed/stop", s.handleComposeManagedStop)
	s.register("POST", "/v1/compose/managed/restart", s.handleComposeManagedRestart)
	s.register("POST", "/v1/compose/managed/down", s.handleComposeManagedDown)
	s.register("POST", "/v1/compose/managed/build", s.handleComposeManagedBuild)

	// ---------- 宿主 compose 管理(面板自身部署栈更新/回滚) ----------
	s.register("GET", "/v1/panel/compose", s.handlePanelCompose) // 面板在线更新读取宿主 compose
	s.register("GET", "/v1/compose/projects", s.handleComposeProjects)
	s.register("GET", "/v1/compose/status", s.handleComposeStatus)
	s.register("GET", "/v1/compose/check_update", s.handleComposeCheckUpdate)
	s.register("POST", "/v1/compose/pull", s.handleComposePull)
	s.register("POST", "/v1/compose/update", s.handleComposeUpdate)
	s.register("POST", "/v1/compose/rollback", s.handleComposeRollback)
	s.register("GET", "/v1/compose/history", s.handleComposeHistory)

	// ---------- binary(Agent 自身更新) ----------
	s.register("GET", "/v1/binary/status", s.handleBinaryStatus)
	s.register("POST", "/v1/binary/check_update", s.handleBinaryCheckUpdate)
	s.register("POST", "/v1/binary/download", s.handleBinaryDownload)
	s.register("POST", "/v1/binary/install", s.handleBinaryInstall)
	s.register("POST", "/v1/binary/rollback", s.handleBinaryRollback)

	// ---------- disk ----------
	s.register("GET", "/v1/disk/usage", s.handleDiskUsage)
	s.register("GET", "/v1/disk/devices", s.handleDiskDevices)
	s.register("GET", "/v1/disk/mounts", s.handleDiskMounts)

	// ---------- sysctl ----------
	s.register("GET", "/v1/sysctl/get", s.handleSysctlGet)
	s.register("POST", "/v1/sysctl/set", s.handleSysctlSet)

	// ---------- network ----------
	s.register("GET", "/v1/network/interfaces", s.handleNetworkInterfaces)
	s.register("GET", "/v1/network/routes", s.handleNetworkRoutes)
	s.register("GET", "/v1/network/dns", s.handleNetworkDNS)

	// ---------- 宿主文件(§55:2026-09-02 移植端点) ----------
	s.register("GET", "/v1/files", s.handleFileList)
	s.register("GET", "/v1/files/entry", s.handleFileEntry)
	s.register("POST", "/v1/files/entries", s.handleFileEntries)
	s.register("GET", "/v1/files/trash", s.handleFileTrashList)
	s.register("GET", "/v1/files/content", s.handleFileContent)
	s.register("PUT", "/v1/files/content", s.handleFileContent)
	s.register("GET", "/v1/files/archive", s.handleFileArchive)
	s.register("GET", "/v1/files/text", s.handleFileText)
	s.register("GET", "/v1/files/tail", s.handleFileTail)
	s.register("POST", "/v1/files/upload", s.handleFileUpload)
	s.register("POST", "/v1/files/actions", s.handleFileAction)

	// ---------- 宿主终端(§56:HTTP 长轮询,2026-09-02 移植) ----------
	s.register("POST", "/v1/host/terminal", s.handleTerminalOpen)
	s.register("GET", "/v1/host/terminal/{id}/output", s.handleTerminalOperationRoute("output"))
	s.register("POST", "/v1/host/terminal/{id}/input", s.handleTerminalOperationRoute("input"))
	s.register("POST", "/v1/host/terminal/{id}/resize", s.handleTerminalOperationRoute("resize"))
	s.register("POST", "/v1/host/terminal/{id}/close", s.handleTerminalOperationRoute("close"))

	// ---------- WebSocket(全部经 Bearer 认证) ----------
	s.registerWS("GET", "/v1/docker/containers/{id}/logs", s.handleContainerLogsWS)
	s.registerWS("GET", "/v1/docker/containers/{id}/stats", s.handleContainerStatsWS)
	s.registerWS("GET", "/v1/compose/managed/logs", s.handleComposeManagedLogsWS)
	s.registerWS("GET", "/v1/docker/events", s.handleDockerEventsWS)
}
