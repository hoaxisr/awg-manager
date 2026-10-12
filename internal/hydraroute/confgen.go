package hydraroute

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// Paths are package-level vars so tests can override them via t.TempDir().
var (
	domainConfPath = "/opt/etc/HydraRoute/domain.conf" //nolint:gochecknoglobals
	ipListPath     = "/opt/etc/HydraRoute/ip.list"     //nolint:gochecknoglobals
)

// SetPaths overrides the HR config file paths. Intended for tests that need
// to point the service at a temp directory; returns a restore function.
func SetPaths(domain, ipList string) (restore func()) {
	origDomain, origIP := domainConfPath, ipListPath
	domainConfPath = domain
	ipListPath = ipList
	return func() {
		domainConfPath = origDomain
		ipListPath = origIP
	}
}

// PathsForTest returns the current HR config file paths (set by SetPaths).
func PathsForTest() (domain, ipList string) { return domainConfPath, ipListPath }

// WriteWholeFile atomically writes content as the entire file body.
// We own the file — anything outside our format is discarded on next write.
func WriteWholeFile(filePath, content string) error {
	if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
		return fmt.Errorf("hydraroute: create parent dir: %w", err)
	}
	return atomicWrite(filePath, content)
}

func atomicWrite(filePath, content string) error {
	return storage.AtomicWrite(filePath, []byte(content))
}
