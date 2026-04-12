package hydraroute

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// geoDataFile is the JSON storage filename for geo data entries.
const geoDataFile = "hydraroute-geodata.json"

// geoDataJSON is the on-disk format for GeoDataStore persistence.
type geoDataJSON struct {
	Files []GeoFileEntry `json:"files"`
}

// GeoDataStore manages .dat file downloads, tracking, and tag caching.
type GeoDataStore struct {
	storagePath string // path to hydraroute-geodata.json
	mu          sync.RWMutex
	entries     []GeoFileEntry
	tagCache    map[string][]GeoTag // path → cached tags
}

// NewGeoDataStore creates a store and loads entries from the JSON file.
func NewGeoDataStore(dataDir string) *GeoDataStore {
	s := &GeoDataStore{
		storagePath: filepath.Join(dataDir, geoDataFile),
		tagCache:    make(map[string][]GeoTag),
	}
	// Best-effort load; errors are silently ignored (empty store is valid).
	_ = s.load()
	return s
}

// List returns a copy of all tracked geo file entries.
func (s *GeoDataStore) List() []GeoFileEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]GeoFileEntry, len(s.entries))
	copy(result, s.entries)
	return result
}

// validateDownloadURL returns an error if rawURL is not a safe http/https URL
// pointing to a public host (not localhost or private IP ranges).
func validateDownloadURL(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("only http/https URLs are allowed")
	}
	if u.Host == "" {
		return fmt.Errorf("URL must have a host")
	}
	host := u.Hostname()
	if host == "localhost" || host == "127.0.0.1" || host == "::1" {
		return fmt.Errorf("localhost URLs are not allowed")
	}
	ip := net.ParseIP(host)
	if ip != nil {
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() {
			return fmt.Errorf("private/local IP addresses are not allowed")
		}
	}
	return nil
}

// Download fetches a .dat file from rawURL, validates it, and tracks it.
func (s *GeoDataStore) Download(fileType, rawURL string) (*GeoFileEntry, error) {
	if fileType != "geosite" && fileType != "geoip" {
		return nil, fmt.Errorf("invalid file type %q: must be geosite or geoip", fileType)
	}

	if err := validateDownloadURL(rawURL); err != nil {
		return nil, fmt.Errorf("invalid download URL: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Count existing entries of this type.
	count := 0
	for _, e := range s.entries {
		if e.Type == fileType {
			count++
		}
	}
	if count >= maxGeoFiles {
		return nil, fmt.Errorf("limit reached: maximum %d %s files allowed", maxGeoFiles, fileType)
	}

	// Derive destination filename from URL, handling conflicts.
	base := filepath.Base(rawURL)
	if base == "" || base == "." || base == "/" {
		base = fileType + ".dat"
	}
	dest := filepath.Join(hrDir, base)
	dest = s.resolveConflict(dest)

	// Download to a temp location then move.
	if err := downloadFile(rawURL, dest); err != nil {
		return nil, fmt.Errorf("download %s: %w", rawURL, err)
	}

	// Validate by parsing the protobuf.
	size, tagCount, err := ReadFileInfo(dest, fileType)
	if err != nil {
		os.Remove(dest)
		return nil, fmt.Errorf("validate %s: %w", dest, err)
	}

	entry := GeoFileEntry{
		Type:     fileType,
		Path:     dest,
		URL:      rawURL,
		Size:     size,
		TagCount: tagCount,
		Updated:  time.Now().UTC().Format(time.RFC3339),
	}

	s.entries = append(s.entries, entry)
	delete(s.tagCache, dest)

	if err := s.saveUnlocked(); err != nil {
		return nil, fmt.Errorf("save metadata: %w", err)
	}

	return &entry, nil
}

// Delete removes the tracked entry and its file from disk.
func (s *GeoDataStore) Delete(path string) error {
	path = filepath.Clean(path)
	if !strings.HasPrefix(path, hrDir) {
		return fmt.Errorf("path outside HydraRoute directory")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	idx := s.findUnlocked(path)
	if idx < 0 {
		return fmt.Errorf("geo file not found: %s", path)
	}

	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove file: %w", err)
	}

	s.entries = append(s.entries[:idx], s.entries[idx+1:]...)
	delete(s.tagCache, path)

	return s.saveUnlocked()
}

// Update re-downloads and revalidates a tracked file from its stored URL.
func (s *GeoDataStore) Update(path string) (*GeoFileEntry, error) {
	path = filepath.Clean(path)
	if !strings.HasPrefix(path, hrDir) {
		return nil, fmt.Errorf("path outside HydraRoute directory")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	idx := s.findUnlocked(path)
	if idx < 0 {
		return nil, fmt.Errorf("geo file not found: %s", path)
	}

	entry := s.entries[idx]

	if err := downloadFile(entry.URL, path); err != nil {
		return nil, fmt.Errorf("re-download %s: %w", entry.URL, err)
	}

	size, tagCount, err := ReadFileInfo(path, entry.Type)
	if err != nil {
		return nil, fmt.Errorf("validate after update: %w", err)
	}

	s.entries[idx].Size = size
	s.entries[idx].TagCount = tagCount
	s.entries[idx].Updated = time.Now().UTC().Format(time.RFC3339)
	delete(s.tagCache, path)

	if err := s.saveUnlocked(); err != nil {
		return nil, fmt.Errorf("save metadata: %w", err)
	}

	updated := s.entries[idx]
	return &updated, nil
}

// UpdateAll updates all tracked files sequentially and returns the count updated.
func (s *GeoDataStore) UpdateAll() (int, error) {
	// Collect paths outside the lock so Update can re-acquire it.
	s.mu.RLock()
	paths := make([]string, len(s.entries))
	for i, e := range s.entries {
		paths[i] = e.Path
	}
	s.mu.RUnlock()

	updated := 0
	var errs []string
	for _, path := range paths {
		if _, err := s.Update(path); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", path, err))
			continue
		}
		updated++
	}

	if len(errs) > 0 {
		return updated, fmt.Errorf("update errors: %s", strings.Join(errs, "; "))
	}
	return updated, nil
}

// GetTags returns the tag list for the given file path, using the cache.
func (s *GeoDataStore) GetTags(path string) ([]GeoTag, error) {
	path = filepath.Clean(path)
	if !strings.HasPrefix(path, hrDir) {
		return nil, fmt.Errorf("path outside HydraRoute directory")
	}

	s.mu.RLock()
	if tags, ok := s.tagCache[path]; ok {
		result := make([]GeoTag, len(tags))
		copy(result, tags)
		s.mu.RUnlock()
		return result, nil
	}

	idx := s.findUnlocked(path)
	if idx < 0 {
		s.mu.RUnlock()
		return nil, fmt.Errorf("geo file not found: %s", path)
	}
	fileType := s.entries[idx].Type
	s.mu.RUnlock()

	// Parse outside the lock (slow protobuf read).
	var tags []GeoTag
	var err error
	switch fileType {
	case "geosite":
		tags, err = ExtractGeoSiteTags(path)
	case "geoip":
		tags, err = ExtractGeoIPTags(path)
	default:
		return nil, fmt.Errorf("unknown file type: %s", fileType)
	}
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	s.tagCache[path] = tags
	s.mu.Unlock()

	result := make([]GeoTag, len(tags))
	copy(result, tags)
	return result, nil
}

// GeoFilePaths returns the tracked file paths grouped by type.
func (s *GeoDataStore) GeoFilePaths() (geoIP, geoSite []string) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, e := range s.entries {
		switch e.Type {
		case "geoip":
			geoIP = append(geoIP, e.Path)
		case "geosite":
			geoSite = append(geoSite, e.Path)
		}
	}
	return geoIP, geoSite
}

// load reads entries from the JSON storage file.
func (s *GeoDataStore) load() error {
	data, err := os.ReadFile(s.storagePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read %s: %w", s.storagePath, err)
	}

	var doc geoDataJSON
	if err := json.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("parse %s: %w", s.storagePath, err)
	}

	s.entries = doc.Files
	return nil
}

// saveUnlocked writes current entries to disk atomically.
// Caller must hold s.mu (write lock).
func (s *GeoDataStore) saveUnlocked() error {
	doc := geoDataJSON{Files: s.entries}

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(doc); err != nil {
		return fmt.Errorf("marshal geo data: %w", err)
	}

	return storage.AtomicWrite(s.storagePath, buf.Bytes())
}

// findUnlocked returns the index of the entry with the given path, or -1.
// Caller must hold s.mu (write lock).
func (s *GeoDataStore) findUnlocked(path string) int {
	for i, e := range s.entries {
		if e.Path == path {
			return i
		}
	}
	return -1
}

// resolveConflict returns a non-conflicting path by appending a numeric suffix.
// Caller must hold s.mu (write lock).
func (s *GeoDataStore) resolveConflict(path string) string {
	ext := filepath.Ext(path)
	base := strings.TrimSuffix(path, ext)

	candidate := path
	for i := 1; ; i++ {
		conflict := false
		for _, e := range s.entries {
			if e.Path == candidate {
				conflict = true
				break
			}
		}
		if !conflict {
			// Also check if file physically exists.
			if _, err := os.Stat(candidate); os.IsNotExist(err) {
				return candidate
			}
		}
		candidate = fmt.Sprintf("%s_%d%s", base, i, ext)
	}
}

// downloadFile downloads rawURL to dest using a 120-second timeout.
// Uses atomic write: downloads to a temp file, then renames.
func downloadFile(rawURL, dest string) error {
	// Defense-in-depth: re-validate scheme before making the request.
	if u, err := url.Parse(rawURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return fmt.Errorf("only http/https URLs are allowed")
	}

	client := &http.Client{Timeout: 120 * time.Second}

	resp, err := client.Get(rawURL) //nolint:noctx
	if err != nil {
		return fmt.Errorf("http get: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status: %s", resp.Status)
	}

	dir := filepath.Dir(dest)
	if err := os.MkdirAll(dir, storage.DirPermission); err != nil {
		return fmt.Errorf("create dir %s: %w", dir, err)
	}

	tmp := fmt.Sprintf("%s.tmp.%d.%d", dest, os.Getpid(), time.Now().UnixNano())
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, storage.FilePermission)
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}

	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("write temp file: %w", err)
	}

	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("close temp file: %w", err)
	}

	if err := os.Rename(tmp, dest); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("rename to dest: %w", err)
	}

	return nil
}
