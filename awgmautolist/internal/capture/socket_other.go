//go:build !linux

package capture

import (
	"fmt"
	"runtime"
)

func openPacketSocket(string) (packetSocket, error) {
	return nil, fmt.Errorf("AF_PACKET is supported only on Linux, not %s", runtime.GOOS)
}
