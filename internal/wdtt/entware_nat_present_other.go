//go:build !linux

package wdtt

import "context"

func entwareNATPresent(_ context.Context, _ string) bool { return true }
