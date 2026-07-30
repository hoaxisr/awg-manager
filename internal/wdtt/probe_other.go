//go:build !linux

package wdtt

import "context"

func probeBinaryHelp(_ context.Context, _ string) (string, error) {
	return "", nil
}
