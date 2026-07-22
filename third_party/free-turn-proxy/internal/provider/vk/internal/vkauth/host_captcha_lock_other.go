//go:build !linux

package vkauth

import (
	"context"

	"github.com/samosvalishe/free-turn-proxy/internal/logx"
)

func acquireHostCaptchaLock(_ context.Context, _ int, _ logx.Logger) (func(), error) {
	return func() {}, nil
}
