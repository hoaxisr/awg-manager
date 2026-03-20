// Package ndmsinfo provides cached NDMS system information fetched via RCI API.
// Call Init() once at startup; all subsequent Get() calls return cached data.
package ndmsinfo

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
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

const rciURL = "http://localhost:79/rci/show/version"

// Init fetches version info from NDMS RCI API with retry.
// Blocks until NDMS responds or timeout expires.
// Should be called once at startup before any Get() calls.
func Init(ctx context.Context, timeout time.Duration) error {
	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			MaxIdleConns:        10,
			MaxIdleConnsPerHost: 5,
			IdleConnTimeout:     30 * time.Second,
		},
	}
	deadline := time.After(timeout)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	fetch := func() (*VersionInfo, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, rciURL, nil)
		if err != nil {
			return nil, err
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
		}

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, err
		}

		var info VersionInfo
		if err := json.Unmarshal(body, &info); err != nil {
			return nil, fmt.Errorf("parse JSON: %w", err)
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
// ASC is supported starting from 5.01.A.4 (alpha 4+), 5.01.B+ (beta+),
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
		return alphaNum >= 4
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

