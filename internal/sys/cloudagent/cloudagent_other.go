//go:build !linux

package cloudagent

import "context"

func reset(ctx context.Context) (int, error) {
	return 0, ctx.Err()
}
