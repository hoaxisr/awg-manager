//go:build linux

package tgwebproxy

import (
	"os"

	"golang.org/x/sys/unix"
)

func openFileNoFollow(path string, flags int, perm os.FileMode) (*os.File, error) {
	flags |= unix.O_NOFOLLOW
	return os.OpenFile(path, flags, perm)
}
