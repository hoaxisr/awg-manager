package hydraroute

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

// Ground-Zerro/Geo-Aggregator publishes the canonical .dat files that
// HydraRoute ships by default (daily-refreshed aggregate of v2fly +
// Russian blocklists). Any file the installer drops into hrneo.conf is
// assumed to come from this source, so AdoptExternalFiles prefills the
// URL and the "Обновить" button works without the user re-adding the
// file by hand.
const (
	GroundZerroGeoIPURL   = "https://raw.githubusercontent.com/Ground-Zerro/Geo-Aggregator/main/geodat/geoip_GA.dat"
	GroundZerroGeoSiteURL = "https://raw.githubusercontent.com/Ground-Zerro/Geo-Aggregator/main/geodat/geosite_GA.dat"
)

// defaultURLForType returns the Ground-Zerro source URL for a known .dat
// type, or "" for unsupported types.
func defaultURLForType(fileType string) string {
	switch fileType {
	case "geoip":
		return GroundZerroGeoIPURL
	case "geosite":
		return GroundZerroGeoSiteURL
	default:
		return ""
	}
}

// geoDataJSON is the on-disk format for GeoDataStore persistence.
type geoDataJSON struct {
	Files []GeoFileEntry `json:"files"`
}

// ProgressFn receives streaming download progress: bytes copied so far and
// total expected (0 if Content-Length was absent). Called from the goroutine
// that drives the HTTP response body — must not block.
type ProgressFn func(downloaded, total int64)

// GeoDataStore manages .dat file downloads, tracking, and tag caching.
type GeoDataStore struct {
	storagePath string // path to hydraroute-geodata.json
	mu          sync.RWMutex
	entries     []GeoFileEntry
	tagCache    map[string][]GeoTag // path → cached tags
	progress    func(rawURL, fileType, phase string, downloaded, total int64, errMsg string)
}

// SetProgressReporter wires a callback that receives lifecycle events for
// each download (start, periodic progress, validate, done/error). Optional;
// nil is fine — no reporting.
func (s *GeoDataStore) SetProgressReporter(fn func(rawURL, fileType, phase string, downloaded, total int64, errMsg string)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.progress = fn
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

	progress := s.progress
	report := func(phase string, downloaded, total int64, errMsg string) {
		if progress != nil {
			progress(rawURL, fileType, phase, downloaded, total, errMsg)
		}
	}

	// Stream the download with progress callbacks.
	bytesProgress := func(downloaded, total int64) {
		report("download", downloaded, total, "")
	}
	if _, err := downloadFile(rawURL, dest, bytesProgress); err != nil {
		report("error", 0, 0, err.Error())
		return nil, fmt.Errorf("download %s: %w", rawURL, err)
	}

	report("validate", 0, 0, "")

	// Validate by parsing the protobuf. Refuses an empty file (often a sign
	// that the user picked the wrong type for this URL).
	size, tagCount, err := ReadFileInfo(dest, fileType)
	if err != nil {
		os.Remove(dest)
		report("error", 0, 0, err.Error())
		return nil, fmt.Errorf("validate %s: %w", dest, err)
	}
	if tagCount == 0 {
		os.Remove(dest)
		emsg := fmt.Sprintf("file has 0 %s entries — wrong type or corrupt download?", fileType)
		report("error", size, size, emsg)
		return nil, fmt.Errorf("%s", emsg)
	}
	report("done", size, size, "")

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
	if entry.URL == "" {
		return nil, fmt.Errorf("cannot update external file: no source URL on record")
	}

	progress := s.progress
	bytesProgress := func(downloaded, total int64) {
		if progress != nil {
			progress(entry.URL, entry.Type, "download", downloaded, total, "")
		}
	}
	if _, err := downloadFile(entry.URL, path, bytesProgress); err != nil {
		if progress != nil {
			progress(entry.URL, entry.Type, "error", 0, 0, err.Error())
		}
		return nil, fmt.Errorf("re-download %s: %w", entry.URL, err)
	}
	if progress != nil {
		progress(entry.URL, entry.Type, "done", 0, 0, "")
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

	// After a successful parse, persist the TagCount + Mtime to the store
	// so the UI shows a useful count across restarts (without re-parsing the
	// .dat file on every service start).
	var persist bool
	s.mu.Lock()
	s.tagCache[path] = tags
	if idx := s.findUnlocked(path); idx >= 0 {
		info, statErr := os.Stat(path)
		if statErr == nil {
			mtime := info.ModTime().UTC().Format(time.RFC3339)
			if s.entries[idx].TagCount != len(tags) ||
				s.entries[idx].Size != info.Size() ||
				s.entries[idx].Mtime != mtime {
				s.entries[idx].TagCount = len(tags)
				s.entries[idx].Size = info.Size()
				s.entries[idx].Mtime = mtime
				persist = true
			}
		}
	}
	if persist {
		// Best-effort — if save fails the in-memory cache is still correct;
		// the count will just re-compute on next service start.
		_ = s.saveUnlocked()
	}
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

// AdoptExternalFiles scans the provided hrneo config for GeoSite/GeoIP file
// paths not yet tracked by this store, and registers them as External entries.
// Returns the number of files adopted.
//
// Adopted entries have URL="" and External=true — the user can delete them
// (which removes the .dat from disk) but cannot "update" them because there
// is no source URL on record.
//
// Only paths under hrDir are adopted. This mirrors the Delete path guard so
// that every adopted entry is actually deletable via the UI. Files outside
// hrDir listed in hrneo.conf are silently skipped.
//
// Missing files (listed in config but not present on disk) are skipped.
// Tag counts are read on a best-effort basis; failures leave TagCount=0.
func (s *GeoDataStore) AdoptExternalFiles(cfg *Config) (int, error) {
	if cfg == nil {
		return 0, nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	known := make(map[string]struct{}, len(s.entries))
	for _, e := range s.entries {
		known[filepath.Clean(e.Path)] = struct{}{}
	}

	adopted := 0
	paths := []struct {
		path, fileType string
	}{}
	for _, p := range cfg.GeoIPFiles {
		paths = append(paths, struct{ path, fileType string }{p, "geoip"})
	}
	for _, p := range cfg.GeoSiteFiles {
		paths = append(paths, struct{ path, fileType string }{p, "geosite"})
	}

	for _, item := range paths {
		clean := filepath.Clean(item.path)
		if _, ok := known[clean]; ok {
			continue
		}
		if !strings.HasPrefix(clean, hrDir) {
			// Delete() refuses paths outside hrDir, so adopting them would
			// yield undeletable entries. Skip.
			continue
		}
		info, err := os.Stat(clean)
		if err != nil {
			// File in config but absent on disk — ignore silently.
			continue
		}
		// IMPORTANT: do not parse the file here. On routers with low RAM a
		// 66 MB geosite.dat read at startup evicts the squashfs page cache
		// and stalls NDM. TagCount is left at 0 and populated lazily on the
		// first GetTags call. Size comes from stat — no I/O beyond metadata.
		mtime := info.ModTime().UTC().Format(time.RFC3339)
		s.entries = append(s.entries, GeoFileEntry{
			Type:     item.fileType,
			Path:     clean,
			URL:      defaultURLForType(item.fileType),
			Size:     info.Size(),
			TagCount: 0,
			Updated:  mtime,
			External: true,
			Mtime:    mtime,
		})
		known[clean] = struct{}{}
		adopted++
	}

	if adopted == 0 {
		return 0, nil
	}
	if err := s.saveUnlocked(); err != nil {
		return adopted, fmt.Errorf("save adopted entries: %w", err)
	}
	return adopted, nil
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

// maxGeoFileSize caps single .dat downloads. Realistic geosite/geoip files
// are 5-30 MB; anything past 200 MB is almost certainly a misclick on a
// random URL, and on a router with limited disk it can fill /opt.
const maxGeoFileSize = 200 << 20 // 200 MB

// progressReader wraps an io.Reader and calls onProgress periodically with
// the cumulative bytes read so far. Throttled — emits at most every ~64 KB
// or 200 ms (whichever comes first) to keep SSE traffic sane.
type progressReader struct {
	r         io.Reader
	total     int64
	read      int64
	lastEmit  int64
	lastTime  time.Time
	onProgress ProgressFn
}

func (p *progressReader) Read(buf []byte) (int, error) {
	n, err := p.r.Read(buf)
	if n > 0 {
		p.read += int64(n)
		shouldEmit := p.read-p.lastEmit >= 64*1024 ||
			time.Since(p.lastTime) >= 200*time.Millisecond ||
			err == io.EOF
		if shouldEmit && p.onProgress != nil {
			p.onProgress(p.read, p.total)
			p.lastEmit = p.read
			p.lastTime = time.Now()
		}
	}
	return n, err
}

// downloadTimeout caps a single .dat fetch. 200 MB at 0.5 MB/s ≈ 7 min in
// the worst case; 15 min leaves plenty of margin for slow router uplinks
// without letting a runaway request hang forever.
const downloadTimeout = 15 * time.Minute

// downloadFile downloads rawURL to dest with a generous overall timeout and a
// hard size cap. Uses atomic write: downloads to a temp file, then renames.
// onProgress (optional) receives streaming byte counters during the copy.
func downloadFile(rawURL, dest string, onProgress ProgressFn) (size int64, err error) {
	// Defense-in-depth: re-validate scheme before making the request.
	if u, parseErr := url.Parse(rawURL); parseErr != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return 0, fmt.Errorf("only http/https URLs are allowed")
	}

	// Per-request context so the timeout covers headers + body. The
	// http.Client.Timeout field has the same effect, but a context lets us
	// surface a clearer error string ("download timed out after …").
	ctx, cancel := context.WithTimeout(context.Background(), downloadTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return 0, fmt.Errorf("build request: %w", err)
	}

	client := &http.Client{}

	resp, err := client.Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return 0, fmt.Errorf("download timed out after %s — slow link or unresponsive server", downloadTimeout)
		}
		return 0, fmt.Errorf("http get: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("unexpected status: %s", resp.Status)
	}

	if resp.ContentLength > maxGeoFileSize {
		return 0, fmt.Errorf("content-length %d > limit %d", resp.ContentLength, maxGeoFileSize)
	}

	dir := filepath.Dir(dest)
	if err := os.MkdirAll(dir, storage.DirPermission); err != nil {
		return 0, fmt.Errorf("create dir %s: %w", dir, err)
	}

	tmp := fmt.Sprintf("%s.tmp.%d.%d", dest, os.Getpid(), time.Now().UnixNano())
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, storage.FilePermission)
	if err != nil {
		return 0, fmt.Errorf("create temp file: %w", err)
	}

	// Cap the read at maxGeoFileSize+1 so we can detect oversize content
	// even when the server didn't send Content-Length.
	src := io.LimitReader(resp.Body, maxGeoFileSize+1)
	pr := &progressReader{
		r:          src,
		total:      resp.ContentLength,
		lastTime:   time.Now(),
		onProgress: onProgress,
	}
	written, err := io.Copy(f, pr)
	if err != nil {
		f.Close()
		os.Remove(tmp)
		if errors.Is(err, context.DeadlineExceeded) {
			return 0, fmt.Errorf("download timed out at %d/%d bytes after %s", pr.read, pr.total, downloadTimeout)
		}
		return 0, fmt.Errorf("write temp file: %w", err)
	}
	if written > maxGeoFileSize {
		f.Close()
		os.Remove(tmp)
		return 0, fmt.Errorf("download exceeds %d bytes", maxGeoFileSize)
	}

	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return 0, fmt.Errorf("close temp file: %w", err)
	}

	if err := os.Rename(tmp, dest); err != nil {
		os.Remove(tmp)
		return 0, fmt.Errorf("rename to dest: %w", err)
	}

	return written, nil
}
