//go:build !linux

package wdtt

import "context"

func entwareNATPresent(_ context.Context, _, _ string) bool { return true }

func entwareNATPresentForServer(_ context.Context, _ ServerConfig, _ string) bool { return true }
