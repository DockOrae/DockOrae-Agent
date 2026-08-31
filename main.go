// DockOrae-Agent — DockOrae 宿主机控制平面(Host Control Plane)。
// 定位:经过严格授权的宿主机操作(swap/systemd/network/sysctl/docker/compose/binary update)。
// 不提供 Web UI、不负责用户/License/业务数据库,仅经 Unix Socket 接受 DockOrae 主程序调用。
// Agent 不能脱离 DockOrae 独立工作:无有效 token 拒绝启动,能力仅经 DockOrae 调用。
package main

import (
	"github.com/DockOrae/DockOrae-Agent/cmd"
)

func main() {
	cmd.Execute()
}
