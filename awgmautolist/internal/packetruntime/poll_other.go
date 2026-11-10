//go:build !linux

package packetruntime

import (
	"context"
	"time"

	"github.com/hoaxisr/awg-manager/awgmautolist/internal/packetcap"
)

func pollFDs(context.Context, []int, time.Duration) ([]bool, error) {
	return nil, packetcap.ErrUnsupported
}
