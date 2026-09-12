package router

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/internal/sys/httpclient"
)

// ruleSetCacheTTL is how long a downloaded remote rule-set stays valid in
// the on-disk cache before getOrDownload fetches a fresh copy.
const ruleSetCacheTTL = 1 * time.Hour

// ruleSetDownloadTimeout caps the HTTP fetch for a remote rule-set.
const ruleSetDownloadTimeout = 30 * time.Second

// ruleSetCache is a sha256-keyed on-disk cache of downloaded rule-set
// files. Keyed by URL, the value is the absolute path to the cached
// file. Concurrent reads are safe; one writer at a time per URL.
//
// We keep the cache OUTSIDE the awg-manager config tree (defaults to
// $TMPDIR/awgm-router-rulesets) so the inspector never pollutes the
// router's persistent state — the cache is a transient implementation
// detail of the inspector.
type ruleSetCache struct {
	cacheDir string

	mu      sync.Mutex
	entries map[string]ruleSetCacheEntry
	// urlLocks serialises downloads per URL so two concurrent Inspect
	// calls don't both fetch the same remote rule-set. Lazily populated
	// on first getOrDownload per URL.
	urlLocks map[string]*sync.Mutex
}

type ruleSetCacheEntry struct {
	path      string
	expiresAt time.Time
}

// newRuleSetCache builds a cache rooted at cacheDir. Empty cacheDir uses
// $TMPDIR/awgm-router-rulesets. Directory is created lazily on first
// download — newRuleSetCache itself does NOT touch disk so tests can
// instantiate it freely.
func newRuleSetCache(cacheDir string) *ruleSetCache {
	if cacheDir == "" {
		cacheDir = filepath.Join(os.TempDir(), "awgm-router-rulesets")
	}
	return &ruleSetCache{
		cacheDir: cacheDir,
		entries:  make(map[string]ruleSetCacheEntry),
		urlLocks: make(map[string]*sync.Mutex),
	}
}

// urlLock returns (creating if needed) the per-URL serialisation mutex.
// Holding this mutex from cache-check through download-and-publish makes
// concurrent getOrDownload calls for the same URL deduplicate to one
// download.
func (c *ruleSetCache) urlLock(url string) *sync.Mutex {
	c.mu.Lock()
	defer c.mu.Unlock()
	if l, ok := c.urlLocks[url]; ok {
		return l
	}
	l := &sync.Mutex{}
	c.urlLocks[url] = l
	return l
}

// httpClient is package-level so tests can swap it via the unexported
// ruleSetHTTPClient hook. Do not swap from tests using t.Parallel().
// Built on the canonical httpclient base for the pinned HTTP/1.1 ALPN +
// ForceAttemptHTTP2=false — rule-set mirrors like raw.githubusercontent.com
// return EOF/malformed h2 otherwise.
var ruleSetHTTPClient = newRuleSetHTTPClient()

func newRuleSetHTTPClient() *http.Client {
	c := &http.Client{Timeout: ruleSetDownloadTimeout}
	// Прокси окружения наследуется СОЗНАТЕЛЬНО (Proxy оставлен нулевым,
	// httpclient.ProxyInheritEnv): это загрузка rule-set'а из интернета,
	// и прокси, прописанный владельцем роутера в окружении демона, уважить
	// правильно. Требования «только напрямую» тут нет.
	if tr, err := httpclient.NewTransport(httpclient.TransportConfig{}); err == nil && tr != nil {
		c.Transport = tr
	}
	return c
}

// getOrDownload returns the local file path for url, downloading and
// caching it on first call (or after the TTL expires). Format only
// influences the cache filename extension — the file content is whatever
// the URL serves.
func (c *ruleSetCache) getOrDownload(url, format string, emit InspectProgressFunc, tag string) (string, error) {
	if emit != nil {
		emit(InspectProgress{Phase: "rule_set_cache_check", Message: fmt.Sprintf("Проверяем cache rule_set %s", tag), RuleSetTag: tag})
	}
	hash := sha256.Sum256([]byte(url))
	cacheKey := hex.EncodeToString(hash[:8])
	ext := ".srs"
	if format == "source" {
		ext = ".json"
	}
	filename := cacheKey + ext

	// Serialise downloads of the same URL — without this, two concurrent
	// Inspect calls would both go to the network. Different URLs still
	// download in parallel.
	urlMu := c.urlLock(url)
	urlMu.Lock()
	defer urlMu.Unlock()

	c.mu.Lock()
	if entry, ok := c.entries[url]; ok && time.Now().Before(entry.expiresAt) {
		if _, err := os.Stat(entry.path); err == nil {
			path := entry.path
			c.mu.Unlock()
			if emit != nil {
				emit(InspectProgress{Phase: "rule_set_cache_hit", Message: fmt.Sprintf("Используем cache rule_set %s", tag), RuleSetTag: tag})
			}
			return path, nil
		}
		// File vanished — fall through and re-download.
	}
	c.mu.Unlock()

	if err := os.MkdirAll(c.cacheDir, 0755); err != nil {
		return "", fmt.Errorf("mkdir cache: %w", err)
	}
	filePath := filepath.Join(c.cacheDir, filename)

	if emit != nil {
		emit(InspectProgress{Phase: "rule_set_download_start", Message: fmt.Sprintf("Скачиваем rule_set %s…", tag), RuleSetTag: tag})
	}
	resp, err := ruleSetHTTPClient.Get(url)
	if err != nil {
		if emit != nil {
			emit(InspectProgress{Phase: "rule_set_download_error", Message: fmt.Sprintf("Ошибка скачивания rule_set %s", tag), RuleSetTag: tag})
		}
		return "", fmt.Errorf("http get: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		if emit != nil {
			emit(InspectProgress{Phase: "rule_set_download_error", Message: fmt.Sprintf("Ошибка скачивания rule_set %s", tag), RuleSetTag: tag})
		}
		return "", fmt.Errorf("download %s: %s", url, resp.Status)
	}

	tmp, err := os.CreateTemp(c.cacheDir, "ruleset-*.tmp")
	if err != nil {
		return "", fmt.Errorf("create temp: %w", err)
	}
	tmpPath := tmp.Name()
	written, err := io.Copy(tmp, resp.Body)
	if err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("copy: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("sync: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("close: %w", err)
	}
	if written == 0 {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("downloaded 0 bytes from %s", url)
	}
	// Atomic publish — rename within the same dir is atomic on POSIX.
	if err := os.Rename(tmpPath, filePath); err != nil {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("rename: %w", err)
	}

	c.mu.Lock()
	c.entries[url] = ruleSetCacheEntry{
		path:      filePath,
		expiresAt: time.Now().Add(ruleSetCacheTTL),
	}
	c.mu.Unlock()
	if emit != nil {
		emit(InspectProgress{Phase: "rule_set_download_done", Message: fmt.Sprintf("rule_set %s скачан", tag), RuleSetTag: tag})
	}

	return filePath, nil
}

// ruleSetMatchExec is the injectable exec hook for tests. Production
// path runs sing-box for real. Tests assign a fake function that
// returns canned stdout/stderr/err. Do not swap from tests using
// t.Parallel() — there is no mutex protecting the swap.
var ruleSetMatchExec = func(binary string, args []string) (stdout, stderr string, err error) {
	cmd := exec.Command(binary, args...)
	var so, se bytes.Buffer
	cmd.Stdout = &so
	cmd.Stderr = &se
	err = cmd.Run()
	return so.String(), se.String(), err
}

// matchRuleSet shells out to `sing-box rule-set match -f <format> <file>
// <input>` and reports whether the rule set matched.
//
// Returns (matched, supported, err):
//   - matched=true means the rule set contains a matching entry
//   - supported=false means we couldn't even attempt the check (binary
//     missing, file missing, type unrecognised, format unsupported,
//     download failed). The caller surfaces this in the per-rule reason
//     and the result-level Note but does NOT treat it as an error.
//   - err is reserved for genuinely unexpected failures (e.g. sing-box
//     wrote real diagnostic output on stderr that does not contain a
//     match marker). Even then, callers prefer to surface the message
//     via Note rather than aborting Inspect.
func matchRuleSet(input string, rs RuleSet, singboxBinary string, cache *ruleSetCache, emit InspectProgressFunc) (matched, supported bool, err error) {
	if singboxBinary == "" {
		return false, false, nil
	}

	var ruleSetPath, format string
	switch rs.Type {
	case "remote":
		if rs.URL == "" {
			return false, false, nil
		}
		format = rs.Format
		if format == "" {
			format = inferFormat(rs.URL)
		}
		if cache == nil {
			return false, false, nil
		}
		p, dlErr := cache.getOrDownload(rs.URL, format, emit, rs.Tag)
		if dlErr != nil {
			return false, false, fmt.Errorf("download: %w", dlErr)
		}
		ruleSetPath = p
	case "local":
		if rs.Path == "" {
			return false, false, nil
		}
		ruleSetPath = rs.Path
		format = rs.Format
		if format == "" {
			format = inferFormat(rs.Path)
		}
	default:
		return false, false, nil
	}

	if _, statErr := os.Stat(ruleSetPath); statErr != nil {
		if emit != nil {
			emit(InspectProgress{Phase: "rule_set_local_check", Message: fmt.Sprintf("Файл rule_set %s не найден", rs.Tag), RuleSetTag: rs.Tag})
		}
		return false, false, nil
	}
	if emit != nil {
		emit(InspectProgress{Phase: "rule_set_match_start", Message: fmt.Sprintf("Проверяем rule_set %s через sing-box…", rs.Tag), RuleSetTag: rs.Tag})
	}

	if format == "" {
		format = "binary"
	}
	args := []string{"rule-set", "match", "-f", format, ruleSetPath, input}
	stdout, stderr, runErr := ruleSetMatchExec(singboxBinary, args)
	stdoutTrim := strings.TrimSpace(stdout)
	stderrTrim := strings.TrimSpace(stderr)
	hasMarker := strings.Contains(stderrTrim, "match rules.") ||
		strings.Contains(stdoutTrim, "match rules.")

	if runErr != nil {
		// exec.Error wraps "binary not found / not executable" — that's
		// a setup problem (the operator's `binary` path is wrong), so we
		// flag it as unsupported rather than a hard error.
		if _, ok := runErr.(*exec.Error); ok {
			return false, false, nil
		}
		// Otherwise treat as a sing-box exit (typical *exec.ExitError, or
		// any test stub). sing-box uses non-zero exit for both "no match"
		// and "match" inconsistently — the textual marker is authoritative.
		if hasMarker {
			return true, true, nil
		}
		// Non-zero exit with non-empty stderr that lacks the marker AND
		// is not the "no match" structure → real error worth surfacing.
		if stderrTrim != "" && !strings.Contains(strings.ToLower(stderrTrim), "match") {
			return false, true, fmt.Errorf("sing-box: %s", stderrTrim)
		}
		return false, true, nil
	}

	return hasMarker, true, nil
}

// inferFormat guesses the rule-set file format from a path or URL
// extension. .srs → binary; .json → source; otherwise empty (caller
// defaults to "binary").
func inferFormat(s string) string {
	low := strings.ToLower(s)
	switch {
	case strings.HasSuffix(low, ".srs"):
		return "binary"
	case strings.HasSuffix(low, ".json"):
		return "source"
	}
	return ""
}
