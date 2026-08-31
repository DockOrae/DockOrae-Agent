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

	// ---------- compose ----------
	s.register("GET", "/v1/compose/projects", s.handleComposeProjects)
	s.register("GET", "/v1/compose/status", s.handleComposeStatus)
	s.register("GET", "/v1/compose/check_update", s.handleComposeCheckUpdate)
	s.register("POST", "/v1/compose/pull", s.handleComposePull)
	s.register("POST", "/v1/compose/update", s.handleComposeUpdate)
	s.register("POST", "/v1/compose/rollback", s.handleComposeRollback)
	s.register("GET", "/v1/compose/history", s.handleComposeHistory)

	// ---------- binary ----------
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

	// ---------- firewall ----------
	s.register("GET", "/v1/firewall/status", s.handleFirewallStatus)
	s.register("GET", "/v1/firewall/rules", s.handleFirewallRules)
	s.register("POST", "/v1/firewall/add", s.handleFirewallAdd)
	s.register("POST", "/v1/firewall/delete", s.handleFirewallDelete)

	// ---------- network ----------
	s.register("GET", "/v1/network/interfaces", s.handleNetworkInterfaces)
	s.register("GET", "/v1/network/routes", s.handleNetworkRoutes)
	s.register("GET", "/v1/network/dns", s.handleNetworkDNS)
}
