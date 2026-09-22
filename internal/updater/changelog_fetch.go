package updater

import (
	"context"
	"fmt"
	"sync"
	"time"

	"net/http"

	"github.com/hoaxisr/awg-manager/internal/downloader"
	"github.com/hoaxisr/awg-manager/internal/sys/appver"
)

// changelogURLForChannel возвращает URL CHANGELOG.md для канала. develop
// отдаётся из /develop. База берётся из entwareRepoURL (переопределяема в
// тестах), чтобы остаться согласованной с Packages.gz.
func changelogURLForChannel(channel string) string {
	if channel == channelDevelop {
		return entwareRepoURL + "/develop/CHANGELOG.md"
	}
	return entwareRepoURL + "/CHANGELOG.md"
}

// changelogFetcher pulls the monolithic CHANGELOG.md, parses it, and
// serves cached results. Single-flight via fetchMu so a slow HTTP call
// converges to one real request under concurrent load.
type changelogFetcher struct {
	url        string
	ttl        time.Duration
	downloader Downloader

	fetchMu sync.Mutex
	mu      sync.RWMutex
	cached  map[string]Entry
	fetched time.Time
}

func newChangelogFetcher(url string, ttl time.Duration, dl Downloader) *changelogFetcher {
	return &changelogFetcher{url: url, ttl: ttl, downloader: dl}
}

// Fetch returns the parsed changelog map. Fresh cache hits skip HTTP;
// errors do not populate the cache so the next call retries.
func (c *changelogFetcher) Fetch(ctx context.Context) (map[string]Entry, error) {
	if entries, ok := c.peek(); ok {
		return entries, nil
	}

	c.fetchMu.Lock()
	defer c.fetchMu.Unlock()

	// Re-check after acquiring the mutex — another goroutine may have
	// populated the cache while we waited.
	if entries, ok := c.peek(); ok {
		return entries, nil
	}

	body, err := c.download(ctx)
	if err != nil {
		return nil, err
	}
	parsed, err := ParseChangelog(body)
	if err != nil {
		return nil, fmt.Errorf("parse changelog: %w", err)
	}
	c.store(parsed)
	return parsed, nil
}

// Invalidate forces the next Fetch to hit the network.
func (c *changelogFetcher) Invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cached = nil
}

// SetURL переключает источник changelog (например при смене канала).
// Сброс кэша гарантирует, что следующий Fetch ударит по новому URL.
func (c *changelogFetcher) SetURL(url string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.url != url {
		c.url = url
		c.cached = nil
	}
}

func (c *changelogFetcher) peek() (map[string]Entry, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.cached == nil || time.Since(c.fetched) > c.ttl {
		return nil, false
	}
	return c.cached, true
}

func (c *changelogFetcher) store(entries map[string]Entry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cached = entries
	c.fetched = time.Now()
}

func (c *changelogFetcher) download(ctx context.Context) (string, error) {
	dl := c.downloader
	if dl == nil {
		dl = newDefaultDownloader()
	}
	c.mu.RLock()
	url := c.url
	c.mu.RUnlock()
	body, meta, err := dl.ReadAll(ctx, downloader.Request{
		Purpose:       "awgm-changelog",
		UserAgent:     appver.UA(),
		URL:           url,
		Method:        http.MethodGet,
		Timeout:       repoTimeout,
		MaxBodyBytes:  changelogMaxBytes,
		AllowedStatus: []int{http.StatusOK, http.StatusNotFound},
	})
	if err != nil {
		return "", err
	}
	if meta.StatusCode == http.StatusNotFound {
		return "", fmt.Errorf("changelog not published yet")
	}
	if meta.StatusCode != http.StatusOK {
		return "", fmt.Errorf("changelog status %d", meta.StatusCode)
	}
	return string(body), nil
}
