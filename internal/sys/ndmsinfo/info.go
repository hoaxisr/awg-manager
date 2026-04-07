// Package ndmsinfo provides cached NDMS system information fetched via RCI API.
// Call Init() once at startup; all subsequent Get() calls return cached data.
package ndmsinfo

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/internal/rci"
)

// VersionInfo holds parsed response from /rci/show/version.
type VersionInfo struct {
	Release      string `json:"release"`
	Title        string `json:"title"`
	Arch         string `json:"arch"`
	HwID         string `json:"hw_id"`
	HwType       string `json:"hw_type"`
	Model        string `json:"model"`
	Device       string `json:"device"`
	Manufacturer string `json:"manufacturer"`
	Vendor       string `json:"vendor"`
	Series       string `json:"series"`
	NDW          struct {
		Components string `json:"components"`
	} `json:"ndw"`
}

var (
	cached *VersionInfo
	mu     sync.RWMutex
)

// Init fetches version info from NDMS RCI API with retry.
// Blocks until NDMS responds or timeout expires.
// Should be called once at startup before any Get() calls.
func Init(ctx context.Context, timeout time.Duration) error {
	client := rci.NewWithTimeout(5 * time.Second)
	deadline := time.After(timeout)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	fetch := func() (*VersionInfo, error) {
		var info VersionInfo
		if err := client.Get(ctx, "/show/version", &info); err != nil {
			return nil, err
		}
		return &info, nil
	}

	// Try immediately
	if info, err := fetch(); err == nil {
		mu.Lock()
		cached = info
		mu.Unlock()
		return nil
	}

	// Retry until timeout
	for {
		select {
		case <-deadline:
			return fmt.Errorf("NDMS not available after %s", timeout)
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if info, err := fetch(); err == nil {
				mu.Lock()
				cached = info
				mu.Unlock()
				return nil
			}
		}
	}
}

// Get returns cached version info, or nil if Init was not called or failed.
func Get() *VersionInfo {
	mu.RLock()
	defer mu.RUnlock()
	return cached
}

// HasComponent checks if the given component name is present in the NDW components list.
func HasComponent(name string) bool {
	info := Get()
	if info == nil || info.NDW.Components == "" {
		return false
	}
	for _, c := range strings.Split(info.NDW.Components, ",") {
		if c == name {
			return true
		}
	}
	return false
}

// HasWireguardComponent returns true if the NDMS firmware has the
// "wireguard" component installed. Required for the nativewg backend
// (NDMS-managed Wireguard interfaces).
func HasWireguardComponent() bool {
	return HasComponent("wireguard")
}

// HasPingCheckComponent returns true if the NDMS firmware has the
// "pingcheck" component installed. Required for NDMS-native ping-check
// profiles used by the nativewg backend. Kernel backend uses a custom
// loop and does not depend on this component.
func HasPingCheckComponent() bool {
	return HasComponent("pingcheck")
}

// SupportsWireguardASC returns true if the current NDMS release supports
// WireGuard as an ASC (Application Service Component).
func SupportsWireguardASC() bool {
	info := Get()
	if info == nil || info.Release == "" {
		return false
	}
	return supportsASC(info.Release)
}

// supportsASC checks if the given NDMS release version supports ASC.
// ASC is supported starting from 5.01.A.3 (alpha 3+), 5.01.B+ (beta+),
// 5.01.03+ (release), or any 5.02+ / 6.x+.
func supportsASC(release string) bool {
	parts := strings.Split(release, ".")
	if len(parts) < 3 {
		return false
	}
	major, _ := strconv.Atoi(parts[0])
	minor, _ := strconv.Atoi(parts[1])
	if major > 5 {
		return true
	}
	if major < 5 || minor < 1 {
		return false
	}
	if minor > 1 {
		return true
	}
	// major == 5, minor == 1
	stage := parts[2]
	if stage == "A" {
		if len(parts) < 4 {
			return false
		}
		alphaNum, _ := strconv.Atoi(parts[3])
		return alphaNum >= 3
	}
	return true
}

// SupportsHRanges returns true if the current NDMS release supports
// H1-H4 header parameters as ranges (AWG 2.0).
// Supported starting from 5.01.A.3 (alpha 3+), 5.01.B+ (beta+),
// 5.01.03+ (release), or any 5.02+ / 6.x+.
func SupportsHRanges() bool {
	info := Get()
	if info == nil || info.Release == "" {
		return false
	}
	return supportsHRanges(info.Release)
}

func supportsHRanges(release string) bool {
	parts := strings.Split(release, ".")
	if len(parts) < 3 {
		return false
	}
	major, _ := strconv.Atoi(parts[0])
	minor, _ := strconv.Atoi(parts[1])
	if major > 5 {
		return true
	}
	if major < 5 || minor < 1 {
		return false
	}
	if minor > 1 {
		return true
	}
	// major == 5, minor == 1
	stage := parts[2]
	if stage == "A" {
		if len(parts) < 4 {
			return false
		}
		alphaNum, _ := strconv.Atoi(parts[3])
		return alphaNum >= 3
	}
	return true
}

// Reset clears the cached version info. Used in tests.
func Reset() {
	mu.Lock()
	cached = nil
	mu.Unlock()
}

