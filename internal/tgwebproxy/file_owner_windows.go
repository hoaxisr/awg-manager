//go:build windows

package tgwebproxy

import "os"

func getFileOwnership(fi os.FileInfo) (int, int) {
	return 0, 0
}

func setFileOwnership(path string, uid, gid int) error {
	return nil
}
