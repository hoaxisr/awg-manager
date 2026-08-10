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
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/sys/httpclient"
	"github.com/hoaxisr/awg-manager/internal/sys/httpdownload"
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
	geoDir      string // managed .dat files: <data-dir>/geo
	mu          sync.RWMutex
	entries     []GeoFileEntry
	tagCache    map[string][]GeoTag // path → cached tags
	busyPaths   map[string]struct{}
	reserved    map[string]string // path -> fileType
	progress    func(rawURL, fileType, phase string, downloaded, total int64, errMsg string)
	appLog      *logging.ScopedLogger
	onChange    func()
}

var (
	updateArtifactRe = regexp.MustCompile(`^(.+)\.update\.(\d+)\.(\d+)$`) //nolint:gochecknoglobals
	backupArtifactRe = regexp.MustCompile(`^(.+)\.backup\.(\d+)\.(\d+)$`) //nolint:gochecknoglobals
	// test hook only: allows deterministic synchronization around update swap window
	geoUpdateSwapHook = func(string) {} //nolint:gochecknoglobals
)

// SetAppLogger wires the UI-visible logger into the store. Optional;
// nil-safe. The store keeps the lazy-construction guarantee for tests
// (NewGeoDataStore takes no logging dep).
func (s *GeoDataStore) SetAppLogger(appLogger logging.AppLogger) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.appLog = logging.NewScopedLogger(appLogger, logging.GroupRouting, logging.SubHrNeo)
}

// SetProgressReporter wires a callback that receives lifecycle events for
// each download (start, periodic progress, validate, done/error). Optional;
// nil is fine — no reporting.
func (s *GeoDataStore) SetProgressReporter(fn func(rawURL, fileType, phase string, downloaded, total int64, errMsg string)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.progress = fn
}

// SetOnChange регистрирует колбэк изменения состава/содержимого геофайлов
// (Update / UpdateAll / Delete / TakeControl). Потребитель — bypass-набор:
// его содержимое выведено из .dat, поэтому обязано пересобираться следом.
// Optional; nil — уведомлений нет.
func (s *GeoDataStore) SetOnChange(fn func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onChange = fn
}

// notifyChange дёргает колбэк в отдельной горутине — потребитель читает те же
// геофайлы через сам стор и не должен ждать под его мьютексом.
func (s *GeoDataStore) notifyChange() {
	s.mu.RLock()
	defer s.mu.RUnlock()
	s.notifyChangeLocked()
}

// notifyChangeLocked — то же для мест, где s.mu уже удерживается (RWMutex не
// реентрантен, повторный захват из notifyChange был бы дедлоком).
func (s *GeoDataStore) notifyChangeLocked() {
	if s.onChange != nil {
		go s.onChange()
	}
}

// NewGeoDataStore creates a store and loads entries from the JSON file.
func NewGeoDataStore(dataDir string) *GeoDataStore {
	geoDir := filepath.Join(dataDir, geoSubdir)
	s := &GeoDataStore{
		storagePath: filepath.Join(dataDir, geoDataFile),
		geoDir:      geoDir,
		tagCache:    make(map[string][]GeoTag),
		busyPaths:   make(map[string]struct{}),
		reserved:    make(map[string]string),
	}
	_ = os.MkdirAll(geoDir, storage.DirPermission)
	s.recoverUpdateArtifacts()
	// Best-effort load; errors are silently ignored (empty store is valid).
	_ = s.load()
	return s
}

// List returns a copy of all tracked geo file entries.
// Reconciles first so manual deletes on disk and stale JSON are reflected
// without requiring a service restart (UI refresh calls this path).
func (s *GeoDataStore) List() []GeoFileEntry {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.reconcileUnlocked() {
		_ = s.saveUnlocked()
	}

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
	return s.DownloadWithClient(context.Background(), fileType, rawURL, nil)
}

// DownloadWithClient fetches a .dat file from rawURL using the provided
// client (or direct client when nil), validates it, and tracks it.
func (s *GeoDataStore) DownloadWithClient(ctx context.Context, fileType, rawURL string, client *http.Client) (*GeoFileEntry, error) {
	return s.DownloadWithClientVia(ctx, fileType, rawURL, client, "")
}

// DownloadWithClientVia is DownloadWithClient plus a UI-visible route label
// for app logging. The caller is responsible for passing the route used by
// the provided HTTP client.
func (s *GeoDataStore) DownloadWithClientVia(ctx context.Context, fileType, rawURL string, client *http.Client, routeLabel string) (*GeoFileEntry, error) {
	if fileType != "geosite" && fileType != "geoip" {
		err := fmt.Errorf("invalid file type %q: must be geosite or geoip", fileType)
		s.logGeoDataError("download-url", rawURL, "Ошибка загрузки geo-data", routeLabel, err)
		return nil, err
	}

	if err := validateDownloadURL(rawURL); err != nil {
		err = fmt.Errorf("invalid download URL: %w", err)
		s.logGeoDataError("download-url", rawURL, "Ошибка загрузки geo-data", routeLabel, err)
		return nil, err
	}

	s.mu.Lock()

	// Count existing + in-flight reserved entries of this type.
	count := 0
	for _, e := range s.entries {
		if e.Type == fileType {
			count++
		}
	}
	for _, t := range s.reserved {
		if t == fileType {
			count++
		}
	}
	if count >= maxGeoFiles {
		s.mu.Unlock()
		err := fmt.Errorf("limit reached: maximum %d %s files allowed", maxGeoFiles, fileType)
		s.logGeoDataError("download-url", rawURL, "Ошибка загрузки geo-data", routeLabel, err)
		return nil, err
	}

	// Derive destination filename from URL, handling conflicts.
	base := filepath.Base(rawURL)
	if base == "" || base == "." || base == "/" {
		base = fileType + ".dat"
	}
	dest := filepath.Join(s.geoDir, base)
	dest = s.resolveConflictLocked(dest)

	progress := s.progress
	s.reserved[dest] = fileType
	s.mu.Unlock()
	report := func(phase string, downloaded, total int64, errMsg string) {
		if progress != nil {
			progress(rawURL, fileType, phase, downloaded, total, errMsg)
		}
	}

	// Stream the download with progress callbacks.
	bytesProgress := func(downloaded, total int64) {
		report("download", downloaded, total, "")
	}
	unreserve := func() {
		s.mu.Lock()
		delete(s.reserved, dest)
		s.mu.Unlock()
	}
	if _, err := downloadFileWithClient(ctx, client, rawURL, dest, bytesProgress); err != nil {
		_ = os.Remove(dest)
		unreserve()
		report("error", 0, 0, err.Error())
		s.logWarn("download", rawURL, fmt.Sprintf("%s: %v", fileType, err))
		s.logGeoDataError("download-url", rawURL, "Ошибка загрузки geo-data", routeLabel, err)
		return nil, fmt.Errorf("download %s: %w", rawURL, err)
	}

	report("validate", 0, 0, "")

	// Validate by parsing the protobuf. Refuses an empty file (often a sign
	// that the user picked the wrong type for this URL).
	size, tagCount, err := ReadFileInfo(dest, fileType)
	if err != nil {
		_ = os.Remove(dest)
		unreserve()
		report("error", 0, 0, err.Error())
		s.logWarn("validate", rawURL, fmt.Sprintf("%s: %v", fileType, err))
		s.logGeoDataError("download-url", rawURL, "Ошибка загрузки geo-data", routeLabel, err)
		return nil, fmt.Errorf("validate %s: %w", dest, err)
	}
	if tagCount == 0 {
		_ = os.Remove(dest)
		unreserve()
		emsg := fmt.Sprintf("file has 0 %s entries — wrong type or corrupt download?", fileType)
		report("error", size, size, emsg)
		s.logWarn("validate", rawURL, emsg)
		err := fmt.Errorf("%s", emsg)
		s.logGeoDataError("download-url", rawURL, "Ошибка загрузки geo-data", routeLabel, err)
		return nil, err
	}
	entry := GeoFileEntry{
		Type:     fileType,
		Path:     dest,
		URL:      rawURL,
		Size:     size,
		TagCount: tagCount,
		Updated:  time.Now().UTC().Format(time.RFC3339),
	}

	s.mu.Lock()
	if _, ok := s.reserved[dest]; !ok {
		s.mu.Unlock()
		_ = os.Remove(dest)
		emsg := fmt.Sprintf("download reservation lost for %s", dest)
		report("error", 0, 0, emsg)
		s.logWarn("save", rawURL, emsg)
		err := fmt.Errorf("download reservation lost for %s", dest)
		s.logGeoDataError("download-url", rawURL, "Ошибка загрузки geo-data", routeLabel, err)
		return nil, err
	}
	delete(s.reserved, dest)
	s.entries = append(s.entries, entry)
	delete(s.tagCache, dest)

	if err := s.saveUnlocked(); err != nil {
		s.entries = s.entries[:len(s.entries)-1]
		delete(s.tagCache, dest)
		s.mu.Unlock()
		_ = os.Remove(dest)
		emsg := fmt.Sprintf("save metadata: %v", err)
		report("error", 0, 0, emsg)
		s.logWarn("save", rawURL, emsg)
		s.logGeoDataError("download-url", rawURL, "Ошибка загрузки geo-data", routeLabel, err)
		return nil, fmt.Errorf("save metadata: %w", err)
	}
	s.mu.Unlock()

	report("done", size, size, "")
	s.logInfo("download-url", rawURL, fmt.Sprintf("Загрузка geo-data через %s: %s", normalizeRouteLabel(routeLabel), rawURL))
	s.logInfo("download", rawURL, fmt.Sprintf("%s downloaded: %d bytes, %d tags", fileType, size, tagCount))
	return &entry, nil
}

// TakeControl moves an external HydraRoute file into awg-manager/geo and
// clears the External flag so awg-manager owns the on-disk copy.
func (s *GeoDataStore) TakeControl(path string) (*GeoFileEntry, error) {
	path = filepath.Clean(path)
	if !s.isManagedPath(path) {
		return nil, fmt.Errorf("path outside managed geo directories")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	idx := s.findUnlocked(path)
	if idx < 0 {
		return nil, fmt.Errorf("geo file not found: %s", path)
	}
	if _, busy := s.busyPaths[path]; busy {
		return nil, fmt.Errorf("geo file is already being updated")
	}
	entry := s.entries[idx]
	if !entry.External {
		return nil, fmt.Errorf("file is already managed by awg-manager")
	}
	if !s.isHRPath(path) {
		return nil, fmt.Errorf("take control only applies to files in HydraRoute directory")
	}

	dest, err := s.relocateIntoGeoDirLocked(path)
	if err != nil {
		return nil, err
	}
	if tags, ok := s.tagCache[path]; ok {
		s.tagCache[dest] = tags
		delete(s.tagCache, path)
	}

	entry.Path = dest
	entry.External = false
	if entry.URL == "" {
		entry.URL = defaultURLForType(entry.Type)
	}
	s.entries[idx] = entry

	if err := s.saveUnlocked(); err != nil {
		return nil, fmt.Errorf("save metadata: %w", err)
	}
	s.notifyChangeLocked() // s.mu удерживается до выхода (defer)
	updated := entry
	return &updated, nil
}

// Delete removes the tracked entry and its file from disk.
func (s *GeoDataStore) Delete(path string) error {
	path = filepath.Clean(path)
	if !s.isManagedPath(path) {
		return fmt.Errorf("path outside managed geo directories")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	idx := s.findUnlocked(path)
	if idx < 0 {
		return fmt.Errorf("geo file not found: %s", path)
	}
	if _, busy := s.busyPaths[path]; busy {
		return fmt.Errorf("geo file is already being updated")
	}

	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove file: %w", err)
	}

	s.entries = append(s.entries[:idx], s.entries[idx+1:]...)
	delete(s.tagCache, path)

	if err := s.saveUnlocked(); err != nil {
		return err
	}
	s.notifyChangeLocked() // s.mu удерживается до выхода (defer)
	return nil
}

// Update re-downloads and revalidates a tracked file from its stored URL.
func (s *GeoDataStore) Update(path string) (*GeoFileEntry, error) {
	return s.UpdateWithClient(context.Background(), path, nil)
}

// UpdateWithClient re-downloads a tracked geo file via the provided client
// (or direct client when nil).
func (s *GeoDataStore) UpdateWithClient(ctx context.Context, path string, client *http.Client) (*GeoFileEntry, error) {
	return s.UpdateWithClientVia(ctx, path, client, "")
}

// UpdateWithClientVia is UpdateWithClient plus a UI-visible route label
// for app logging. The caller is responsible for passing the route used by
// the provided HTTP client.
func (s *GeoDataStore) UpdateWithClientVia(ctx context.Context, path string, client *http.Client, routeLabel string) (*GeoFileEntry, error) {
	return s.updateWithClientVia(ctx, path, client, routeLabel, true)
}

// updateWithClientVia — тело обновления одного файла. notify=false использует
// UpdateAll: он уведомляет один раз за весь прогон, а не по файлу (иначе
// single-flight потребителя проглотил бы уведомления о файлах, обновлённых
// пока идёт пересборка по первому).
func (s *GeoDataStore) updateWithClientVia(ctx context.Context, path string, client *http.Client, routeLabel string, notify bool) (*GeoFileEntry, error) {
	path = filepath.Clean(path)
	if !s.isManagedPath(path) {
		return nil, fmt.Errorf("path outside managed geo directories")
	}

	s.mu.Lock()

	idx := s.findUnlocked(path)
	if idx < 0 {
		s.mu.Unlock()
		err := fmt.Errorf("geo file not found: %s", path)
		s.logGeoDataError("update-url", path, "Ошибка обновления geo-data", routeLabel, err)
		return nil, err
	}
	if _, busy := s.busyPaths[path]; busy {
		s.mu.Unlock()
		err := fmt.Errorf("geo file is already being updated")
		s.logGeoDataError("update-url", path, "Ошибка обновления geo-data", routeLabel, err)
		return nil, err
	}

	entry := s.entries[idx]
	if entry.External {
		s.mu.Unlock()
		err := fmt.Errorf("cannot update external file managed by HydraRoute Neo — use «Взять под управление or update in HR Neo")
		s.logGeoDataError("update-url", path, "Ошибка обновления geo-data", routeLabel, err)
		return nil, err
	}
	sourceURL := entry.URL
	if sourceURL == "" {
		sourceURL = defaultURLForType(entry.Type)
	}
	if sourceURL == "" {
		s.mu.Unlock()
		err := fmt.Errorf("no source URL for %s file", entry.Type)
		s.logGeoDataError("update-url", path, "Ошибка обновления geo-data", routeLabel, err)
		return nil, err
	}
	progress := s.progress
	s.busyPaths[path] = struct{}{}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.busyPaths, path)
		s.mu.Unlock()
	}()
	bytesProgress := func(downloaded, total int64) {
		if progress != nil {
			progress(sourceURL, entry.Type, "download", downloaded, total, "")
		}
	}
	candidate := fmt.Sprintf("%s.update.%d.%d", path, os.Getpid(), time.Now().UnixNano())
	if _, err := downloadFileWithClient(ctx, client, sourceURL, candidate, bytesProgress); err != nil {
		_ = os.Remove(candidate)
		if progress != nil {
			progress(sourceURL, entry.Type, "error", 0, 0, err.Error())
		}
		s.logGeoDataError("update-url", sourceURL, "Ошибка обновления geo-data", routeLabel, err)
		return nil, fmt.Errorf("re-download %s: %w", sourceURL, err)
	}
	if progress != nil {
		progress(sourceURL, entry.Type, "validate", 0, 0, "")
	}

	size, tagCount, err := ReadFileInfo(candidate, entry.Type)
	if err != nil {
		_ = os.Remove(candidate)
		if progress != nil {
			progress(sourceURL, entry.Type, "error", 0, 0, err.Error())
		}
		s.logGeoDataError("update-url", sourceURL, "Ошибка обновления geo-data", routeLabel, err)
		return nil, fmt.Errorf("validate after update: %w", err)
	}
	if tagCount == 0 {
		_ = os.Remove(candidate)
		emsg := fmt.Sprintf("file has 0 %s entries — wrong type or corrupt download?", entry.Type)
		if progress != nil {
			progress(sourceURL, entry.Type, "error", size, size, emsg)
		}
		err := fmt.Errorf("%s", emsg)
		s.logGeoDataError("update-url", sourceURL, "Ошибка обновления geo-data", routeLabel, err)
		return nil, err
	}

	s.mu.Lock()
	idx = s.findUnlocked(path)
	if idx < 0 {
		s.mu.Unlock()
		_ = os.Remove(candidate)
		emsg := fmt.Sprintf("geo file not found after update: %s", path)
		if progress != nil {
			progress(sourceURL, entry.Type, "error", 0, 0, emsg)
		}
		err := fmt.Errorf("geo file not found after update: %s", path)
		s.logGeoDataError("update-url", sourceURL, "Ошибка обновления geo-data", routeLabel, err)
		return nil, err
	}
	geoUpdateSwapHook("before_swap_locked")
	backup := fmt.Sprintf("%s.backup.%d.%d", path, os.Getpid(), time.Now().UnixNano())
	if err := os.Rename(path, backup); err != nil {
		s.mu.Unlock()
		_ = os.Remove(candidate)
		if progress != nil {
			progress(sourceURL, entry.Type, "error", 0, 0, err.Error())
		}
		s.logGeoDataError("update-url", sourceURL, "Ошибка обновления geo-data", routeLabel, err)
		return nil, fmt.Errorf("backup current geo file: %w", err)
	}
	geoUpdateSwapHook("after_backup_rename")
	if err := os.Rename(candidate, path); err != nil {
		restoreErr := os.Rename(backup, path)
		s.mu.Unlock()
		_ = os.Remove(candidate)
		emsg := err.Error()
		if restoreErr != nil {
			emsg = fmt.Sprintf("%s; rollback file restore failed: %v", emsg, restoreErr)
			s.logWarn("rollback", sourceURL, emsg)
		}
		if progress != nil {
			progress(sourceURL, entry.Type, "error", 0, 0, emsg)
		}
		s.logGeoDataError("update-url", sourceURL, "Ошибка обновления geo-data", routeLabel, err)
		return nil, fmt.Errorf("replace geo file: %w", err)
	}

	oldEntry := s.entries[idx]
	oldTags, hadTags := s.tagCache[path]
	s.entries[idx].Size = size
	s.entries[idx].TagCount = tagCount
	s.entries[idx].Updated = time.Now().UTC().Format(time.RFC3339)
	delete(s.tagCache, path)

	if err := s.saveUnlocked(); err != nil {
		s.entries[idx] = oldEntry
		if hadTags {
			s.tagCache[path] = oldTags
		} else {
			delete(s.tagCache, path)
		}
		rollbackWarn := ""
		_ = os.Remove(path)
		restoreErr := os.Rename(backup, path)
		emsg := fmt.Sprintf("save metadata: %v", err)
		if restoreErr != nil {
			emsg = fmt.Sprintf("%s; rollback file restore failed: %v", emsg, restoreErr)
			rollbackWarn = emsg
		}
		s.mu.Unlock()
		if progress != nil {
			progress(sourceURL, entry.Type, "error", 0, 0, emsg)
		}
		if rollbackWarn != "" {
			s.logWarn("rollback", sourceURL, rollbackWarn)
		}
		s.logGeoDataError("update-url", sourceURL, "Ошибка обновления geo-data", routeLabel, err)
		return nil, fmt.Errorf("save metadata: %w", err)
	}

	updated := s.entries[idx]
	s.mu.Unlock()
	if err := os.Remove(backup); err != nil && !os.IsNotExist(err) {
		s.logWarn("cleanup", sourceURL, fmt.Sprintf("remove backup file: %v", err))
	}
	if progress != nil {
		progress(sourceURL, entry.Type, "done", size, size, "")
	}
	s.logInfo("update-url", sourceURL, fmt.Sprintf("Обновление geo-data через %s: %s", normalizeRouteLabel(routeLabel), sourceURL))
	if notify {
		s.notifyChange()
	}
	return &updated, nil
}

func (s *GeoDataStore) recoverUpdateArtifacts() {
	entries, err := os.ReadDir(s.geoDir)
	if err != nil {
		return
	}
	for _, de := range entries {
		if de.IsDir() {
			continue
		}
		full := filepath.Join(s.geoDir, de.Name())
		original, kind, ok := originalPathFromUpdateArtifact(full)
		if !ok {
			continue
		}
		switch kind {
		case "update":
			_ = os.Remove(full)
		case "backup":
			if _, statErr := os.Stat(original); os.IsNotExist(statErr) {
				_ = os.Rename(full, original)
			} else {
				_ = os.Remove(full)
			}
		}
	}
}

func originalPathFromUpdateArtifact(path string) (original string, kind string, ok bool) {
	base := filepath.Base(path)
	if m := updateArtifactRe.FindStringSubmatch(base); len(m) == 4 {
		return filepath.Join(filepath.Dir(path), m[1]), "update", true
	}
	if m := backupArtifactRe.FindStringSubmatch(base); len(m) == 4 {
		return filepath.Join(filepath.Dir(path), m[1]), "backup", true
	}
	return "", "", false
}

// UpdateAll updates all non-external tracked files sequentially.
func (s *GeoDataStore) UpdateAll() (int, error) {
	return s.UpdateAllWithClient(context.Background(), nil)
}

// UpdateAllWithClient refreshes all tracked geo files via the provided client
// (or direct client when nil).
func (s *GeoDataStore) UpdateAllWithClient(ctx context.Context, client *http.Client) (int, error) {
	return s.UpdateAllWithClientVia(ctx, client, "")
}

// UpdateAllWithClientVia is UpdateAllWithClient plus a UI-visible route label
// for app logging. The caller is responsible for passing the route used by
// the provided HTTP client.
func (s *GeoDataStore) UpdateAllWithClientVia(ctx context.Context, client *http.Client, routeLabel string) (int, error) {
	// Collect paths outside the lock so Update can re-acquire it.
	s.mu.RLock()
	var paths []string
	for _, e := range s.entries {
		if e.External {
			continue
		}
		paths = append(paths, e.Path)
	}
	s.mu.RUnlock()

	updated := 0
	var errs []string
	for _, path := range paths {
		if _, err := s.updateWithClientVia(ctx, path, client, routeLabel, false); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", path, err))
			continue
		}
		updated++
	}
	if updated > 0 {
		s.notifyChange()
	}

	if len(errs) > 0 {
		return updated, fmt.Errorf("update errors: %s", strings.Join(errs, "; "))
	}
	return updated, nil
}

func normalizeRouteLabel(routeLabel string) string {
	routeLabel = strings.TrimSpace(routeLabel)
	if routeLabel == "" {
		return "direct"
	}
	return routeLabel
}

func (s *GeoDataStore) logGeoDataError(action, rawURL, prefix, routeLabel string, err error) {
	if err == nil {
		return
	}
	s.logWarn(action, rawURL, fmt.Sprintf("%s через %s: %s: %v", prefix, normalizeRouteLabel(routeLabel), rawURL, err))
}

// GetTags returns the tag list for the given file path, using the cache.
func (s *GeoDataStore) GetTags(path string) ([]GeoTag, error) {
	path = filepath.Clean(path)
	if !s.isManagedPath(path) {
		return nil, fmt.Errorf("path outside managed geo directories")
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

// GeoFilePaths returns tracked paths for hrneo.conf sync (deduped, stable order).
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
	return dedupeGeoPaths(geoIP), dedupeGeoPaths(geoSite)
}

// GeoIPTagCounts суммирует Count каждого geoip-тега по всем отслеживаемым
// geoip-файлам. Источник бюджет-валидации bypass-тегов; ошибки чтения
// отдельного файла пропускаются (валидация консервативна и без него).
func (s *GeoDataStore) GeoIPTagCounts() map[string]int {
	geoIP, _ := s.GeoFilePaths()
	out := make(map[string]int)
	for _, p := range geoIP {
		tags, err := s.GetTags(p)
		if err != nil {
			continue
		}
		for _, t := range tags {
			out[strings.ToLower(t.Name)] += t.Count
		}
	}
	return out
}

// AdoptExternalFiles scans the provided hrneo config for GeoSite/GeoIP file
// paths not yet tracked by this store, and registers them as External entries.
// Returns the number of files adopted.
//
// HR paths are marked External=true with a default Ground-Zerro URL when the
// type is known, so "Обновить" can re-download in place. "Взять под управление"
// moves the file into awg-manager/geo and clears External.
//
// Paths under awg-manager/geo or /opt/etc/HydraRoute are adopted in place
// (HR-downloaded files stay in HydraRoute). Paths outside those dirs are skipped.
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
		clean := resolveGeoConfigPath(item.path)
		if clean == "" {
			continue
		}
		if _, ok := known[clean]; ok {
			continue
		}
		if !hasPathPrefix(clean, s.geoDir) && !hasPathPrefix(clean, hrDir) {
			continue
		}
		managed := clean
		if _, ok := known[managed]; ok {
			continue
		}
		info, err := os.Stat(managed)
		if err != nil {
			continue
		}
		// IMPORTANT: do not parse the file here. On routers with low RAM a
		// 66 MB geosite.dat read at startup evicts the squashfs page cache
		// and stalls NDM. TagCount is left at 0 and populated lazily on the
		// first GetTags call. Size comes from stat — no I/O beyond metadata.
		mtime := info.ModTime().UTC().Format(time.RFC3339)
		s.entries = append(s.entries, GeoFileEntry{
			Type:     item.fileType,
			Path:     managed,
			URL:      defaultURLForType(item.fileType),
			Size:     info.Size(),
			TagCount: 0,
			Updated:  mtime,
			External: s.isHRPath(managed),
			Mtime:    mtime,
		})
		known[managed] = struct{}{}
		adopted++
	}

	if s.reconcileUnlocked() || adopted > 0 {
		if err := s.saveUnlocked(); err != nil {
			return adopted, fmt.Errorf("save adopted entries: %w", err)
		}
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
		// Quarantine so the next List()→save doesn't persist an empty
		// catalog over the recoverable file (losing custom source URLs).
		storage.QuarantineCorrupt(s.storagePath, err)
		return fmt.Errorf("parse %s: %w", s.storagePath, err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = doc.Files
	if s.reconcileUnlocked() {
		_ = s.saveUnlocked()
	}
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
func (s *GeoDataStore) resolveConflictLocked(path string) string {
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
		if _, reserved := s.reserved[candidate]; reserved {
			conflict = true
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

// resolveConflict returns a non-conflicting path.
// Caller must hold s.mu (write lock).
func (s *GeoDataStore) resolveConflict(path string) string {
	return s.resolveConflictLocked(path)
}

func (s *GeoDataStore) logWarn(action, key, msg string) {
	s.mu.RLock()
	l := s.appLog
	s.mu.RUnlock()
	if l != nil {
		l.Warn(action, key, msg)
	}
}

func (s *GeoDataStore) logInfo(action, key, msg string) {
	s.mu.RLock()
	l := s.appLog
	s.mu.RUnlock()
	if l != nil {
		l.Info(action, key, msg)
	}
}

// maxGeoFileSize caps single .dat downloads. Realistic geosite/geoip files
// are 5-30 MB; anything past 200 MB is almost certainly a misclick on a
// random URL, and on a router with limited disk it can fill /opt.
const maxGeoFileSize = 200 << 20 // 200 MB

// downloadTimeout caps a single .dat fetch. 200 MB at 0.5 MB/s ≈ 7 min in
// the worst case; 15 min leaves plenty of margin for slow router uplinks
// without letting a runaway request hang forever.
const downloadTimeout = 15 * time.Minute

func downloadFileWithClient(ctx context.Context, client *http.Client, rawURL, dest string, onProgress ProgressFn) (size int64, err error) {
	// Defense-in-depth: re-validate scheme before making the request.
	if u, parseErr := url.Parse(rawURL); parseErr != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return 0, fmt.Errorf("only http/https URLs are allowed")
	}

	// Per-request context so the timeout covers headers + body. The
	// http.Client.Timeout field has the same effect, but a context lets us
	// surface a clearer error string ("download timed out after …").
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, downloadTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return 0, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("User-Agent", "curl/8.7.1")

	if client == nil {
		// Build the direct-download fallback on the canonical httpclient base
		// so it inherits the pinned HTTP/1.1 ALPN + ForceAttemptHTTP2=false —
		// geo mirrors like raw.githubusercontent.com return EOF/malformed h2
		// otherwise (the failure httpclient exists to prevent).
		tr, terr := httpclient.NewTransport(httpclient.TransportConfig{})
		if terr != nil || tr == nil {
			client = &http.Client{}
		} else {
			client = &http.Client{Transport: tr}
		}
	}

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
	pr := httpdownload.NewReader(src, resp.ContentLength, httpdownload.ProgressFn(onProgress))
	written, err := io.Copy(f, pr)
	if err != nil {
		f.Close()
		os.Remove(tmp)
		if errors.Is(err, context.DeadlineExceeded) {
			return 0, fmt.Errorf("download timed out at %d/%d bytes after %s", pr.BytesRead(), resp.ContentLength, downloadTimeout)
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
