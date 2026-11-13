package traffic

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// DefaultSysfsRoot is the standard Linux sysfs network interface root.
// Override via the root argument in tests.
const DefaultSysfsRoot = "/sys/class/net"

// readSysfsCounters reads the rx_bytes and tx_bytes counters for a
// kernel network interface under the given sysfs root (typically
// DefaultSysfsRoot). The error matches os.IsNotExist when the
// interface is absent, so callers can distinguish the expected
// "iface temporarily missing during start/stop" case from malformed
// reads that warrant a warning.
func readSysfsCounters(root, iface string) (rx, tx int64, err error) {
	rx, err = readUint64File(filepath.Join(root, iface, "statistics", "rx_bytes"))
	if err != nil {
		return 0, 0, err
	}
	tx, err = readUint64File(filepath.Join(root, iface, "statistics", "tx_bytes"))
	if err != nil {
		return 0, 0, err
	}
	return rx, tx, nil
}

// ifaceUp — поднят ли интерфейс (бит IFF_UP в <root>/<iface>/flags).
// Нет интерфейса или файл нечитаем — false.
func ifaceUp(root, iface string) bool {
	b, err := os.ReadFile(filepath.Join(root, iface, "flags"))
	if err != nil {
		return false
	}
	flags, err := strconv.ParseUint(strings.TrimSpace(string(b)), 0, 32)
	return err == nil && flags&syscall.IFF_UP != 0
}

func readUint64File(path string) (int64, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	s := strings.TrimSpace(string(b))
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, err
	}
	return n, nil
}
