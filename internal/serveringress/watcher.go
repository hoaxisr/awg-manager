package serveringress

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/internal/cdndispatcher"
	"github.com/hoaxisr/awg-manager/internal/tgwebproxy"
	"github.com/hoaxisr/awg-manager/internal/vlessserver"
)

// ConfigWatcher automatically detects changes to ingress JSON settings on disk
// and hot-reloads the corresponding services without requiring a restart or rebuild.
type ConfigWatcher struct {
	coord        *Coordinator
	mu           sync.Mutex
	running      bool
	cancel       context.CancelFunc
	lastModTime  map[string]time.Time
	lastHash     map[string][32]byte
	pollInterval time.Duration
	wg           sync.WaitGroup
}

func (cw *ConfigWatcher) watchedPaths() []string {
	return []string{
		filepath.Join(cw.coord.dataDir, "xray", "xray-server-settings.json"),
		filepath.Join(cw.coord.dataDir, "tproxy", "tgwebproxy-settings.json"),
		cw.coord.dispatcherConfigPath(),
	}
}

// NewConfigWatcher creates a new ConfigWatcher instance.
func (c *Coordinator) NewConfigWatcher() *ConfigWatcher {
	return &ConfigWatcher{
		coord:       c,
		lastModTime: make(map[string]time.Time),
		lastHash:    make(map[string][32]byte),
	}
}

// StartConfigWatcher starts background polling for configuration changes.
func (c *Coordinator) StartConfigWatcher(ctx context.Context) *ConfigWatcher {
	cw := c.NewConfigWatcher()
	cw.Start(ctx)
	return cw
}

// ReloadFromDisk re-reads configuration files from disk and hot-reloads active ingress runtimes.
func (c *Coordinator) ReloadFromDisk(ctx context.Context) error {
	var errs []error

	// 1. Reload tgwebproxy-settings.json
	tgPath := filepath.Join(c.dataDir, "tproxy", "tgwebproxy-settings.json")
	if data, err := os.ReadFile(tgPath); err == nil && len(data) > 0 {
		var tgCfg tgwebproxy.Config
		if err := json.Unmarshal(data, &tgCfg); err == nil {
			if err := c.ApplyTelegramConfig(ctx, tgCfg); err != nil {
				errs = append(errs, err)
			}
		}
	}

	// 2. Reload xray-server-settings.json
	xrayPath := filepath.Join(c.dataDir, "xray", "xray-server-settings.json")
	if data, err := os.ReadFile(xrayPath); err == nil && len(data) > 0 {
		var xrayCfg vlessserver.Config
		if err := json.Unmarshal(data, &xrayCfg); err == nil {
			if err := c.ApplyXrayConfig(ctx, xrayCfg); err != nil {
				errs = append(errs, err)
			}
		}
	}

	// 3. Reload dispatcher config.json
	dispPath := c.dispatcherConfigPath()
	if data, err := os.ReadFile(dispPath); err == nil && len(data) > 0 {
		var dispCfg cdndispatcher.Config
		if err := json.Unmarshal(data, &dispCfg); err == nil {
			if err := c.ApplyDispatcherConfig(ctx, dispCfg); err != nil {
				errs = append(errs, err)
			}
		}
	}

	return errors.Join(errs...)
}

func (cw *ConfigWatcher) Start(ctx context.Context) {
	cw.mu.Lock()
	if cw.running {
		cw.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(ctx)
	cw.cancel = cancel
	cw.running = true
	interval := cw.pollInterval
	if interval <= 0 {
		interval = 2 * time.Second
	}
	cw.mu.Unlock()

	paths := cw.watchedPaths()
	xrayPath, tgPath, dispPath := paths[0], paths[1], paths[2]

	cw.snapshotPaths(paths...)
	cw.coord.registerWatcher(cw)

	cw.wg.Add(1)
	go func() {
		defer cw.wg.Done()
		cw.pollLoop(ctx, interval, xrayPath, tgPath, dispPath)
	}()
}

func (cw *ConfigWatcher) snapshotPaths(paths ...string) {
	cw.mu.Lock()
	defer cw.mu.Unlock()
	for _, p := range paths {
		if fi, err := os.Stat(p); err == nil {
			cw.lastModTime[p] = fi.ModTime()
			if data, rerr := os.ReadFile(p); rerr == nil && len(data) > 0 {
				cw.lastHash[p] = sha256.Sum256(data)
			}
		}
	}
}

func (cw *ConfigWatcher) Stop() {
	cw.mu.Lock()
	if !cw.running {
		cw.mu.Unlock()
		return
	}
	cw.running = false
	if cw.cancel != nil {
		cw.cancel()
	}
	cw.mu.Unlock()
	cw.wg.Wait()
	if cw.coord != nil {
		cw.coord.unregisterWatcher(cw)
	}
}

func (cw *ConfigWatcher) pollLoop(ctx context.Context, interval time.Duration, xrayPath, tgPath, dispPath string) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	allPaths := []string{xrayPath, tgPath, dispPath}

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cw.checkFile(ctx, tgPath, allPaths, func(data []byte, seq uint64) (bool, error) {
				var cfg tgwebproxy.Config
				if err := json.Unmarshal(data, &cfg); err != nil {
					return false, err
				}
				return cw.coord.applyTelegramConfigSeq(ctx, cfg, &seq)
			})

			cw.checkFile(ctx, xrayPath, allPaths, func(data []byte, seq uint64) (bool, error) {
				var cfg vlessserver.Config
				if err := json.Unmarshal(data, &cfg); err != nil {
					return false, err
				}
				return cw.coord.applyXrayConfigSeq(ctx, cfg, &seq)
			})

			cw.checkFile(ctx, dispPath, allPaths, func(data []byte, seq uint64) (bool, error) {
				var cfg cdndispatcher.Config
				if err := json.Unmarshal(data, &cfg); err != nil {
					return false, err
				}
				return cw.coord.applyDispatcherConfigSeq(ctx, cfg, &seq)
			})
		}
	}
}

func (cw *ConfigWatcher) checkFile(ctx context.Context, path string, allPaths []string, applyFn func([]byte, uint64) (bool, error)) {
	if cw.coord != nil && cw.coord.inTx.Load() > 0 {
		return
	}
	var seqBeforeRead uint64
	if cw.coord != nil {
		seqBeforeRead = cw.coord.txSeq.Load()
	}

	fi, err := os.Stat(path)
	if err != nil || fi.Size() == 0 {
		return
	}

	cw.mu.Lock()
	last, known := cw.lastModTime[path]
	if !known {
		cw.lastModTime[path] = fi.ModTime()
		if data, rerr := os.ReadFile(path); rerr == nil && len(data) > 0 {
			cw.lastHash[path] = sha256.Sum256(data)
		}
		cw.mu.Unlock()
		return
	}
	if !fi.ModTime().After(last) {
		cw.mu.Unlock()
		return
	}
	if cw.coord != nil && (cw.coord.inTx.Load() > 0 || cw.coord.txSeq.Load() != seqBeforeRead) {
		cw.mu.Unlock()
		return
	}
	cw.lastModTime[path] = fi.ModTime()
	prevHash, hasHash := cw.lastHash[path]
	cw.mu.Unlock()

	// Debounce write settling
	select {
	case <-ctx.Done():
		return
	case <-time.After(150 * time.Millisecond):
	}

	if cw.coord != nil && (cw.coord.inTx.Load() > 0 || cw.coord.txSeq.Load() != seqBeforeRead) {
		return
	}

	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 {
		return
	}
	if cw.coord != nil && (cw.coord.inTx.Load() > 0 || cw.coord.txSeq.Load() != seqBeforeRead) {
		return
	}
	curHash := sha256.Sum256(data)
	if hasHash && curHash == prevHash {
		cw.mu.Lock()
		if fiPost, stErr := os.Stat(path); stErr == nil {
			cw.lastModTime[path] = fiPost.ModTime()
		}
		cw.mu.Unlock()
		return
	}

	applied, err := applyFn(data, seqBeforeRead)
	if err != nil {
		log.Printf("[config-watcher] failed to apply updated %s: %v", filepath.Base(path), err)
		return
	}
	if !applied {
		return
	}

	// Update modtime and content hash for all coordinated ingress files after apply to prevent ping-pong loopback triggers
	cw.snapshotPaths(allPaths...)
	log.Printf("[config-watcher] hot-reloaded configuration from %s", filepath.Base(path))
}
