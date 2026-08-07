package wdtt

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	statsLogModeRAM  = "ram"
	statsLogModeOff  = "off"
	statsLogModeDisk = "disk"
)

func normalizeStatsLogMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case statsLogModeOff, statsLogModeDisk:
		return strings.ToLower(strings.TrimSpace(mode))
	default:
		return statsLogModeRAM
	}
}

// redirectServerStatsLog points wdtt-server server.log away from flash.
// The binary rewrites JSON stats every ~2s; default is /tmp symlink.
func redirectServerStatsLog(cfgDir, instanceID, mode string) error {
	mode = normalizeStatsLogMode(mode)
	if mode == statsLogModeDisk {
		return nil
	}
	logPath := filepath.Join(cfgDir, "server.log")
	target := "/dev/null"
	if mode == statsLogModeRAM {
		id := strings.TrimSpace(instanceID)
		if id == "" {
			id = "default"
		}
		target = filepath.Join("/tmp", fmt.Sprintf("awg-wdtt-server-%s.log", id))
	}
	_ = os.Remove(logPath)
	return os.Symlink(target, logPath)
}
