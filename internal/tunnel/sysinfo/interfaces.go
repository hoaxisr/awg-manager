// Package sysinfo provides system-level information about tunnel interfaces.
// This includes detection of external (unmanaged) tunnels and interface enumeration.
package sysinfo

import (
	"context"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/sys/exec"
)

var (
	// External tunnel patterns (not managed by awg-manager)
	opkgtunPattern = regexp.MustCompile(`^opkgtun(\d+)$`)
	awgPattern     = regexp.MustCompile(`^awg(\d+)$`)

	// awg-manager managed tunnel patterns
	// OS 5.x uses opkgtun100+ (same pattern, just different numbers)
	// OS 4.x uses awgmX
	awgmPattern = regexp.MustCompile(`^awgm(\d+)$`)
)

// HasDefaultIPv6Route checks whether the system has a real default IPv6 route.
// Reads /proc/net/ipv6_route looking for ::/0 on a non-loopback interface.
// The kernel always has a ::/0 reject route on lo (metric ffffffff) — we skip it.
func HasDefaultIPv6Route() bool {
	data, err := os.ReadFile("/proc/net/ipv6_route")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		// Format: dest dest_prefix src src_prefix nexthop metric refcnt use flags iface
		// Default route: dest=00000000000000000000000000000000 dest_prefix=00
		if len(fields) >= 10 && fields[0] == "00000000000000000000000000000000" && fields[1] == "00" &&
			strings.TrimSpace(fields[9]) != "lo" {
			return true
		}
	}
	return false
}

// ExtractInterfaceNumber extracts the numeric suffix from an interface name.
// Returns the number and true if the interface matches opkgtunX, awgX, or awgmX pattern.
func ExtractInterfaceNumber(ifaceName string) (int, bool) {
	// Try opkgtun pattern first (OS 5.0+)
	if matches := opkgtunPattern.FindStringSubmatch(ifaceName); matches != nil {
		num, _ := strconv.Atoi(matches[1])
		return num, true
	}
	// Try awgm pattern (awg-manager on OS 4.x)
	if matches := awgmPattern.FindStringSubmatch(ifaceName); matches != nil {
		num, _ := strconv.Atoi(matches[1])
		return num, true
	}
	// Try awg pattern (external tunnels on OS 4.x)
	if matches := awgPattern.FindStringSubmatch(ifaceName); matches != nil {
		num, _ := strconv.Atoi(matches[1])
		return num, true
	}
	return -1, false
}

// ListSystemInterfaces returns a list of tunnel interface numbers found in the system.
// On OS 5.0+: scans for opkgtunX interfaces (external tunnels use 0-99, awg-manager uses 100+)
// On OS 4.x: scans for awgX and awgmX interfaces (external use awgX, awg-manager uses awgmX)
func ListSystemInterfaces() ([]int, error) {
	entries, err := os.ReadDir("/sys/class/net")
	if err != nil {
		return nil, err
	}

	var numbers []int
	for _, entry := range entries {
		name := entry.Name()

		// Check opkgtun pattern (OS 5.x)
		if matches := opkgtunPattern.FindStringSubmatch(name); matches != nil {
			if num, err := strconv.Atoi(matches[1]); err == nil {
				numbers = append(numbers, num)
			}
			continue
		}

		// Check awgm pattern (awg-manager on OS 4.x)
		if matches := awgmPattern.FindStringSubmatch(name); matches != nil {
			if num, err := strconv.Atoi(matches[1]); err == nil {
				// Store as negative to distinguish from awgX
				// This is just for enumeration, actual number extraction is separate
				numbers = append(numbers, num)
			}
			continue
		}

		// Check awg pattern (external on OS 4.x)
		if matches := awgPattern.FindStringSubmatch(name); matches != nil {
			if num, err := strconv.Atoi(matches[1]); err == nil {
				numbers = append(numbers, num)
			}
		}
	}

	return numbers, nil
}

// ExternalTunnelInfo contains information about an external tunnel.
type ExternalTunnelInfo struct {
	InterfaceName string `json:"interfaceName"`
	TunnelNumber  int    `json:"tunnelNumber"`
	IsAWG         bool   `json:"isAWG"`
	PublicKey     string `json:"publicKey,omitempty"`
	Endpoint      string `json:"endpoint,omitempty"`
	LastHandshake string `json:"lastHandshake,omitempty"`
	RxBytes       int64  `json:"rxBytes"`
	TxBytes       int64  `json:"txBytes"`
}

// IsAWGInterface checks if an interface is an AWG tunnel by running awg show.
// Returns detailed info if it's an AWG interface.
func IsAWGInterface(ctx context.Context, ifaceName string) (*ExternalTunnelInfo, bool) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	result, err := exec.Run(ctx, "/opt/sbin/awg", "show", ifaceName)
	if err != nil || result == nil {
		return nil, false
	}

	if !hasAWGOutput(result.Stdout) {
		return nil, false
	}

	num, ok := ExtractInterfaceNumber(ifaceName)
	if !ok {
		return nil, false
	}

	info := &ExternalTunnelInfo{
		InterfaceName: ifaceName,
		TunnelNumber:  num,
		IsAWG:         true,
	}

	// Parse awg show output for additional info
	info.PublicKey = parseField(result.Stdout, "peer")
	info.Endpoint = parseField(result.Stdout, "endpoint")
	info.LastHandshake = parseField(result.Stdout, "latest handshake")

	// Read traffic stats from sysfs
	info.RxBytes = readSysfsInt64("/sys/class/net/" + ifaceName + "/statistics/rx_bytes")
	info.TxBytes = readSysfsInt64("/sys/class/net/" + ifaceName + "/statistics/tx_bytes")

	return info, true
}

// hasAWGOutput checks if awg show output indicates a valid AWG interface.
func hasAWGOutput(output string) bool {
	if output == "" {
		return false
	}
	// Valid AWG output starts with "interface:" at the beginning of a line
	for _, line := range strings.Split(output, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(strings.ToLower(trimmed), "interface:") {
			return true
		}
	}
	return false
}

func parseField(output, field string) string {
	fieldLower := strings.ToLower(field)
	for _, line := range strings.Split(output, "\n") {
		trimmed := strings.TrimSpace(line)
		lineLower := strings.ToLower(trimmed)
		if strings.HasPrefix(lineLower, fieldLower+":") {
			colonIdx := strings.Index(trimmed, ":")
			if colonIdx != -1 && colonIdx+1 < len(trimmed) {
				return strings.TrimSpace(trimmed[colonIdx+1:])
			}
		}
	}
	return ""
}

func readSysfsInt64(path string) int64 {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	val, _ := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	return val
}
