//go:build !linux

package wdtt

import "context"

func (s *Service) syncOpkgPolicyDefaultRoutes(context.Context, string, string) {}
