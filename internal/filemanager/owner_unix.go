//go:build !windows

package filemanager

import (
	"os"
	"strconv"
	"syscall"
)

func fileOwner(info os.FileInfo) (string, string) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", ""
	}
	uid := strconv.FormatUint(uint64(stat.Uid), 10)
	gid := strconv.FormatUint(uint64(stat.Gid), 10)
	if stat.Uid == 0 {
		uid = "root"
	}
	if stat.Gid == 0 {
		gid = "root"
	}
	return uid, gid
}

func preserveFileOwnership(file *os.File, info os.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return nil
	}
	return file.Chown(int(stat.Uid), int(stat.Gid))
}
