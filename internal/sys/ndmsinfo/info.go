// Package ndmsinfo provides cached NDMS system information fetched via RCI API.
// Call Init() once at startup; all subsequent Get() calls return cached data.
package ndmsinfo

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
	client := &http.Client{Timeout: 5 * time.Second}
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

// Reset clears cached data (for tests only).
func Reset() {
	mu.Lock()
	cached = nil
	mu.Unlock()
}
