package connections

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// sysClassNet is the sysfs path for network interfaces. Overridable for testing.
var sysClassNet = "/sys/class/net"

// buildIfindexMap reads /sys/class/net/*/ifindex and returns ifindex → iface name.
func buildIfindexMap() map[int]string {
	result := make(map[int]string)

	entries, err := os.ReadDir(sysClassNet)
	if err != nil {
		return result
	}

	for _, entry := range entries {
		ifaceName := entry.Name()
		data, err := os.ReadFile(filepath.Join(sysClassNet, ifaceName, "ifindex"))
		if err != nil {
			continue
		}
		idx, err := strconv.Atoi(strings.TrimSpace(string(data)))
		if err != nil {
			continue
		}
		result[idx] = ifaceName
	}

	return result
}
