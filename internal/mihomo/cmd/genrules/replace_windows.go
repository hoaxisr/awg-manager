//go:build windows

package main

import (
	"golang.org/x/sys/windows"
)

func replaceFile(src, dst string) error {
	fromPtr, err := windows.UTF16PtrFromString(src)
	if err != nil {
		return err
	}
	toPtr, err := windows.UTF16PtrFromString(dst)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(fromPtr, toPtr, windows.MOVEFILE_REPLACE_EXISTING)
}
