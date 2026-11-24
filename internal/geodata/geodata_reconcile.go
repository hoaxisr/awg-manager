package geodata

import (
	"os"
	"path/filepath"
)

// reconcileUnlocked normalizes the in-memory catalog:
//   - External matches the path (AWGM geo dir → false, HR tree → true)
//   - drops entries whose files are missing on disk
//
// AWGM and HR may each hold their own copy of the same basename; we do not
// collapse those into one catalog entry.
//
// Caller must hold s.mu (write lock). Returns whether entries changed.
func (s *GeoDataStore) reconcileUnlocked() bool {
	changed := false

	for i := range s.entries {
		want := s.isHRPath(s.entries[i].Path)
		if s.entries[i].External != want {
			s.entries[i].External = want
			changed = true
		}
	}

	alive := s.entries[:0]
	for _, e := range s.entries {
		if _, err := os.Stat(e.Path); err != nil {
			delete(s.tagCache, e.Path)
			changed = true
			continue
		}
		alive = append(alive, e)
	}
	s.entries = alive
	return changed
}

// dedupeGeoPaths returns unique paths in first-seen order.
func dedupeGeoPaths(paths []string) []string {
	if len(paths) < 2 {
		return paths
	}
	seen := make(map[string]struct{}, len(paths))
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		clean := filepath.Clean(p)
		if clean == "" {
			continue
		}
		if _, ok := seen[clean]; ok {
			continue
		}
		seen[clean] = struct{}{}
		out = append(out, p)
	}
	return out
}
