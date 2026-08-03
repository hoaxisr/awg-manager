//go:build linux

package wdtt

import (
	"os"
	"path/filepath"
)

type sysNetChecker struct{}

func NewSysNetChecker() InterfaceChecker {
	return sysNetChecker{}
}

func (sysNetChecker) InterfaceExists(name string) bool {
	if name == "" {
		return false
	}
	_, err := os.Stat(filepath.Join("/sys/class/net", name))
	return err == nil
}
