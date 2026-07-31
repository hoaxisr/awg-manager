//go:build linux

package wdtt

import (
	"context"

	"github.com/hoaxisr/awg-manager/internal/sys/exec"
)

func probeBinaryHelp(ctx context.Context, bin string) (string, error) {
	res, err := exec.Run(ctx, bin, "-h")
	if res != nil && res.Stdout != "" {
		return res.Stdout, err
	}
	if res != nil {
		return res.Stderr, err
	}
	return "", err
}
