//go:build !linux

package wdtt

import "context"

func (s *Service) resolveServerEntwareNATExtIface(_ context.Context, _ ServerConfig, _ string) (string, error) {
	return "", nil
}
