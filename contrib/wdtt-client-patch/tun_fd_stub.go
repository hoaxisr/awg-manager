//go:build !linux

package main

import (
	"context"
	"errors"
	"os"
)

func tunFdSockPath() string { return "" }

func recvTunFD(_ context.Context, _ string) (*os.File, error) {
	return nil, errors.New("recvTunFD: linux only")
}
