// Package ndmsinfo provides cached NDMS system information backed by the
// query.SystemInfoStore. Call Init() once at startup with a SystemInfoStore
// reference; all subsequent Get() / HasComponent() / Supports*() calls read
// from that store.
package ndmsinfo

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/ndms/query"
)

var (
	storeMu sync.RWMutex
	store   *query.SystemInfoStore
)

// Init initialises the version store reference and blocks until the
// underlying SystemInfoStore is loaded or the timeout expires. Retries
// every second on failure (e.g. NDMS not yet up at boot).
func Init(ctx context.Context, sysInfo *query.SystemInfoStore, timeout time.Duration) error {
	storeMu.Lock()
	store = sysInfo
	storeMu.Unlock()

	deadline := time.After(timeout)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	if err := sysInfo.Init(ctx); err == nil {
		return nil
	}

	for {
		select {
		case <-deadline:
			return fmt.Errorf("NDMS not available after %s", timeout)
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := sysInfo.Init(ctx); err == nil {
				return nil
			}
		}
	}
}

// Get returns the cached Version, or nil if Init was not called or the
// store is empty.
func Get() *ndms.Version {
	storeMu.RLock()
	s := store
	storeMu.RUnlock()
	if s == nil {
		return nil
	}
	v, err := s.Get()
	if err != nil {
		return nil
	}
	return &v
}

// Reset clears the store reference. Used in tests.
func Reset() {
	storeMu.Lock()
	store = nil
	storeMu.Unlock()
}

// HasComponent checks if the given component name is present in the
// NDW components list.
func HasComponent(name string) bool {
	info := Get()
	if info == nil {
		return false
	}
	for _, c := range info.Components {
		if c == name {
			return true
		}
	}
	return false
}

// HasWireguardComponent returns true if the NDMS firmware has the
// "wireguard" component installed. Required for the nativewg backend.
func HasWireguardComponent() bool {
	return HasComponent("wireguard")
}

// HasPingCheckComponent returns true if the NDMS firmware has the
// "pingcheck" component installed.
func HasPingCheckComponent() bool {
	return HasComponent("pingcheck")
}

// HasProxyComponent returns true if the NDMS firmware has the "proxy"
// component installed.
func HasProxyComponent() bool {
	return HasComponent("proxy")
}

// SupportsWireguardASC returns true if the current NDMS release supports
// WireGuard as an ASC (Application Service Component).
func SupportsWireguardASC() bool {
	info := Get()
	if info == nil || info.Release == "" {
		return false
	}
	return isAtLeast501A3(info.Release)
}

// SupportsHRanges returns true if the current NDMS release supports
// H1-H4 header parameters as ranges (AWG 2.0). Shares the same firmware
// gate as SupportsASC — both features landed in the same release.
func SupportsHRanges() bool {
	info := Get()
	if info == nil || info.Release == "" {
		return false
	}
	return isAtLeast501A3(info.Release)
}

// isAtLeast501A3 returns true when release is >= 5.01.A.3 (alpha 3+),
// 5.01.B+ (beta+), 5.01.03+ (release), or any 5.02+ / 6.x+. Both ASC
// support and H-range support landed in that cut; share one check.
func isAtLeast501A3(release string) bool {
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
