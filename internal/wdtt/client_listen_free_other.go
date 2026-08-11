//go:build !linux

package wdtt

func freeStaleClientListenPort(_ string, _ string) {}
