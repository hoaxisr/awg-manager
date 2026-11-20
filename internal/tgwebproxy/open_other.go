//go:build !linux

package tgwebproxy

import (
	"fmt"
	"os"
)

func openFileNoFollow(path string, flags int, perm os.FileMode) (*os.File, error) {
	fi, err := os.Lstat(path)
	if err == nil {
		if fi.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("%s is a symlink: not allowed", path)
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	return os.OpenFile(path, flags, perm)
}
