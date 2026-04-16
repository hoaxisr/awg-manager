package updater

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// defaultChangelogURL is the full URL to the CHANGELOG.md on the repo
// server.
const defaultChangelogURL = "http://repo.hoaxisr.ru/CHANGELOG.md"

// changelogFetcher pulls the monolithic CHANGELOG.md, parses it, and
// serves cached results. Single-flight via fetchMu so a slow HTTP call
// converges to one real request under concurrent load.
type changelogFetcher struct {
	url string
	ttl time.Duration

	fetchMu sync.Mutex
	mu      sync.RWMutex
	cached  map[string]Entry
	fetched time.Time
}

func newChangelogFetcher(url string, ttl time.Duration) *changelogFetcher {
	return &changelogFetcher{url: url, ttl: ttl}
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
	ctx, cancel := context.WithTimeout(ctx, repoTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return "", fmt.Errorf("changelog not published yet")
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("changelog status %d", resp.StatusCode)
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
