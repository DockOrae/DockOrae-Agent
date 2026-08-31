//go:build !windows

package files

import (
	"os"
	"os/user"
	"strconv"
	"syscall"
)

// ownerGroup Unix:从 st_uid/st_gid 查用户名/组名(nsenter 模式下读取的是宿主的 /etc/passwd)。
func ownerGroup(_ string, fi os.FileInfo) (string, string) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return "", ""
	}
	uid := strconv.FormatUint(uint64(st.Uid), 10)
	gid := strconv.FormatUint(uint64(st.Gid), 10)
	owner, group := uid, gid
	if u, err := user.LookupId(uid); err == nil {
		owner = u.Username
	}
	if g, err := user.LookupGroupId(gid); err == nil {
		group = g.Name
	}
	return owner, group
}
