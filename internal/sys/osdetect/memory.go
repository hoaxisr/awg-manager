package osdetect

import (
	"bufio"
	"os"
	"strconv"
	"strings"
	"sync"
)

// LowMemoryThresholdMB is the threshold below which GC optimizations are applied.
// Routers with 256MB RAM report ~248MB, so we use 200MB to avoid false positives.
const LowMemoryThresholdMB = 200

var (
	totalMemoryMB     int
	totalMemoryOnce   sync.Once
)

// GetTotalMemoryMB returns total system RAM in megabytes.
// The value is cached after first call.
// Returns 0 if unable to determine.
func GetTotalMemoryMB() int {
	totalMemoryOnce.Do(func() {
		totalMemoryMB = detectTotalMemory()
	})
	return totalMemoryMB
}

// IsLowMemoryDevice returns true if the device has less than LowMemoryThresholdMB RAM.
func IsLowMemoryDevice() bool {
	mem := GetTotalMemoryMB()
	return mem > 0 && mem < LowMemoryThresholdMB
}

// GetGCEnv returns environment variables for Go GC tuning.
// If disableMemorySaving is true, returns soft mode (GOGC=100 only).
// If disableMemorySaving is false (default), applies auto mode for low-memory devices.
// Returns nil for devices with sufficient RAM (>= 200MB) in auto mode.
func GetGCEnv(disableMemorySaving bool) []string {
	if disableMemorySaving {
		return []string{"GOGC=100"}
	}

	if !IsLowMemoryDevice() {
		return nil
	}

	mem := GetTotalMemoryMB()

	var memLimit string
	switch {
	case mem < 50:
		memLimit = "16MiB"
	case mem < 100:
		memLimit = "24MiB"
	default:
		memLimit = "32MiB"
	}

	return []string{
		"GOGC=50",
		"GOMEMLIMIT=" + memLimit,
	}
}

// detectTotalMemory reads /proc/meminfo and extracts MemTotal.
func detectTotalMemory() int {
	file, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "MemTotal:") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				kb, err := strconv.Atoi(fields[1])
				if err != nil {
					return 0
				}
				return kb / 1024
			}
		}
	}
	return 0
}

// ResetMemory clears the cached memory detection (for tests only).
func ResetMemory() {
	totalMemoryMB = 0
	totalMemoryOnce = sync.Once{}
}
