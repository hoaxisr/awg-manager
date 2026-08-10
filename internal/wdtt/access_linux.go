//go:build linux

package wdtt

import (
	"os"
	"path/filepath"
	"strings"
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

func (c sysNetChecker) InterfaceOperUp(name string) bool {
	if !c.InterfaceExists(name) {
		return false
	}
	b, err := os.ReadFile(filepath.Join("/sys/class/net", name, "operstate"))
	if err != nil {
		return false
	}
	switch strings.TrimSpace(string(b)) {
	case "up", "unknown":
		return true
	default:
		return false
	}
}
