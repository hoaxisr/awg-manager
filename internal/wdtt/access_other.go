//go:build !linux

package wdtt

func NewSysNetChecker() InterfaceChecker {
	return nil
}
