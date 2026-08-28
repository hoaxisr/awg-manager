package wdttusers

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Режимы журнала статистики форка (Record.StatsLog). Дефолт — ram: форк
// переписывает JSON статистики каждые ~2 с, и на флеш-памяти роутера это
// изнашивающая запись.
const (
	StatsLogModeRAM  = "ram"
	StatsLogModeOff  = "off"
	StatsLogModeDisk = "disk"
)

func normalizeStatsLogMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case StatsLogModeOff, StatsLogModeDisk:
		return strings.ToLower(strings.TrimSpace(mode))
	default:
		return StatsLogModeRAM
	}
}

// redirectServerStatsLog points wdtt-server server.log away from flash.
// The binary rewrites JSON stats every ~2s; default is /tmp symlink.
func redirectServerStatsLog(cfgDir, instanceID, mode string) error {
	mode = normalizeStatsLogMode(mode)
	if mode == StatsLogModeDisk {
		return nil
	}
	logPath := filepath.Join(cfgDir, "server.log")
	target := "/dev/null"
	if mode == StatsLogModeRAM {
		id := strings.TrimSpace(instanceID)
		if id == "" {
			id = "default"
		}
		target = filepath.Join("/tmp", fmt.Sprintf("awg-wdtt-server-%s.log", id))
	}
	_ = os.Remove(logPath)
	return os.Symlink(target, logPath)
}
