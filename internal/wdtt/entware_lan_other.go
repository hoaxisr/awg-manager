//go:build !linux

package wdtt

import "context"

func applyEntwareLAN(_ context.Context, _ string, _ []string, _ AccessManager, _ string) error {
	return nil
}

func removeEntwareLAN(_ context.Context, _ string) {}

func entwareLANPresent(_ context.Context, _ string, _ []string) bool { return true }
