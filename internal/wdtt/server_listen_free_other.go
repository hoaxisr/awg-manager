//go:build !linux

package wdtt

func freeStaleServerListenPorts(_ string, _ ServerConfig) {}
