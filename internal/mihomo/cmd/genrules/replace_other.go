//go:build !windows

package main

import "os"

func replaceFile(src, dst string) error {
	return os.Rename(src, dst)
}
