//go:build !linux

package wdtt

import "context"

func (s *Service) clientTunFdSockPath(_ string) string { return "" }

func sendTunFD(_ context.Context, _, _ string) error { return nil }
