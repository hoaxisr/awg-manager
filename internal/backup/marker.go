package backup

import (
	"os"
	"path/filepath"
)

const PostRestoreMarkerName = "post-restore"

// WritePostRestoreMarker records that the next daemon start should cold-boot
// from restored on-disk config instead of reconnecting to surviving processes.
func WritePostRestoreMarker(dataDir string) error {
	dataDir = filepath.Clean(dataDir)
	runDir := filepath.Join(dataDir, "run")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(runDir, PostRestoreMarkerName), []byte("1\n"), 0o644)
}

// HasPostRestoreMarker reports whether a post-restore cold boot is pending.
func HasPostRestoreMarker(dataDir string) bool {
	_, err := os.Stat(postRestoreMarkerPath(dataDir))
	return err == nil
}

// ConsumePostRestoreMarker removes the marker if present and reports whether it existed.
func ConsumePostRestoreMarker(dataDir string) bool {
	path := postRestoreMarkerPath(dataDir)
	if _, err := os.Stat(path); err != nil {
		return false
	}
	_ = os.Remove(path)
	return true
}

func postRestoreMarkerPath(dataDir string) string {
	return filepath.Join(filepath.Clean(dataDir), "run", PostRestoreMarkerName)
}
