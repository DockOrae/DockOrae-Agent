//go:build windows

package files

import "os"

// ownerGroup Windows(仅本地 Direct 模式测试用):无 uid/gid,返回空。
func ownerGroup(_ string, _ os.FileInfo) (string, string) {
	return "", ""
}
