package service

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
)

const sysfsNetRoot = "/sys/class/net"

// wdttRawKernelState reports live wdtt-raw tunnel state from the kernel iface.
// Registry rows are metadata-only — never routed through tunnel.NewNames.
func wdttRawKernelState(stored *storage.AWGTunnel) tunnel.StateInfo {
	info := tunnel.StateInfo{BackendType: "wdtt-raw", State: tunnel.StateStopped}
	if stored == nil {
		return info
	}
	iface := strings.TrimSpace(stored.RawKernelIface)
	if iface == "" {
		if stored.Enabled {
			info.State = tunnel.StateRunning
		}
		if stored.StartedAt != "" {
			info.ConnectedAt = stored.StartedAt
		}
		return info
	}
	if _, err := os.Stat(filepath.Join(sysfsNetRoot, iface)); err != nil {
		return info
	}
	info.State = tunnel.StateRunning
	info.RxBytes, info.TxBytes = readIfaceByteCounters(iface)
	if stored.StartedAt != "" {
		info.ConnectedAt = stored.StartedAt
	}
	return info
}

func readIfaceByteCounters(iface string) (rx, tx int64) {
	rx = readSysfsCounter(filepath.Join(sysfsNetRoot, iface, "statistics", "rx_bytes"))
	tx = readSysfsCounter(filepath.Join(sysfsNetRoot, iface, "statistics", "tx_bytes"))
	return rx, tx
}

func readSysfsCounter(path string) int64 {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	n, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	if err != nil {
		return 0
	}
	return n
}
