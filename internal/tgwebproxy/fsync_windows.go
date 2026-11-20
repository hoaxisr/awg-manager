//go:build windows

package tgwebproxy

func fsyncDir(dirPath string) error {
	// Windows directory handles do not support fsync via FlushFileBuffers
	return nil
}
