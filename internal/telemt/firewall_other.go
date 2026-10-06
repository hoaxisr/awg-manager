//go:build !linux

package telemt

import "context"

func applyFirewall(ctx context.Context, port int) error {
	return nil
}

func removeFirewall(ctx context.Context, port int) {}
