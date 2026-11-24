package geodata

import (
	"path/filepath"
	"strings"
)

const geoSubdir = "geo"

// hasPathPrefix reports whether clean is equal to prefix or is a child of prefix.
func hasPathPrefix(clean, prefix string) bool {
	clean = filepath.Clean(clean)
	prefix = filepath.Clean(prefix)
	if clean == prefix {
		return true
	}
	sep := string(filepath.Separator)
	return strings.HasPrefix(clean, prefix+sep)
}

// isManagedPath reports whether path is under awg-manager/geo or HydraRoute's
// directory — the only locations catalog entries may point to.
func (s *GeoDataStore) isManagedPath(path string) bool {
	if s == nil {
		return false
	}
	clean := filepath.Clean(path)
	if s.geoDir != "" && hasPathPrefix(clean, s.geoDir) {
		return true
	}
	return hasPathPrefix(clean, hrDir)
}

// isHRPath reports whether path lives under HydraRoute but not awg-manager/geo.
func (s *GeoDataStore) isHRPath(path string) bool {
	if s == nil {
		return false
	}
	clean := filepath.Clean(path)
	if s.geoDir != "" && hasPathPrefix(clean, s.geoDir) {
		return false
	}
	return hasPathPrefix(clean, hrDir)
}
