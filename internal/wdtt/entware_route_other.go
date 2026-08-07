//go:build !linux

package wdtt

import "context"

func ensureWgClientRoute(_ context.Context, _, _ string) error { return nil }

func wgClientRoutePresent(_ context.Context, _, _ string) bool { return true }

func (c ServerConfig) ensureServerWgClientRoute(_ context.Context) error { return nil }
