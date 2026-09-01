//go:build linux

package terminal

import (
	"os"
	"syscall"
)

func terminalExecutableOwnerTrusted(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == 0
}
