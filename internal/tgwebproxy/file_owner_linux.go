//go:build !windows

package tgwebproxy

import (
	"os"
	"syscall"
)

func getFileOwnership(fi os.FileInfo) (int, int) {
	if stat, ok := fi.Sys().(*syscall.Stat_t); ok {
		return int(stat.Uid), int(stat.Gid)
	}
	return 0, 0
}

func setFileOwnership(path string, uid, gid int) error {
	if uid == 0 && gid == 0 {
		return nil
	}
	return os.Chown(path, uid, gid)
}
