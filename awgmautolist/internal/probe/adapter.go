package probe

import (
	"context"

	"github.com/hoaxisr/awg-manager/awgmautolist/internal/model"
)

// PackageFuncs adapts the package functions to the engine's small probe interface.
type PackageFuncs struct{}

func (PackageFuncs) Check(ctx context.Context, target model.Target, path Path) Result {
	return Check(ctx, target, path)
}
func (PackageFuncs) DirectAndWAN(ctx context.Context, target model.Target, url string, path Path) DirectAndWANResult {
	return DirectAndWAN(ctx, target, url, path)
}
func (PackageFuncs) CheckWAN(ctx context.Context, url string, path Path) Result {
	return CheckWAN(ctx, url, path)
}
