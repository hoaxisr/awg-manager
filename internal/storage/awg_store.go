package storage

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/logger"
	"github.com/hoaxisr/awg-manager/internal/sys/lock"
	"github.com/hoaxisr/awg-manager/internal/sys/osdetect"
)

// AWGTunnelStore provides directory-based storage for AmneziaWG tunnel metadata.
type AWGTunnelStore struct {
	dir      string
	log      *logger.Logger
	lockName string
	lockDir  string
	timeout  time.Duration
}

// NewAWGTunnelStore creates a new AWG tunnel store.
func NewAWGTunnelStore(dir string, log *logger.Logger) *AWGTunnelStore {
	return NewAWGTunnelStoreWithLockDir(dir, log, lock.LockDir)
}

// NewAWGTunnelStoreWithLockDir creates a new AWG tunnel store with custom lock directory.
func NewAWGTunnelStoreWithLockDir(dir string, log *logger.Logger, lockDir string) *AWGTunnelStore {
	return &AWGTunnelStore{
		dir:      dir,
		log:      log,
		lockName: "tunnels",
		lockDir:  lockDir,
		timeout:  5 * time.Second,
	}
}

// List returns all AWG tunnels by scanning the directory.
func (s *AWGTunnelStore) List() ([]AWGTunnel, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []AWGTunnel{}, nil
		}
		return nil, fmt.Errorf("read tunnels directory: %w", err)
	}

	var tunnels []AWGTunnel
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}

		path := filepath.Join(s.dir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}

		var tunnel AWGTunnel
		if err := json.Unmarshal(data, &tunnel); err != nil {
			continue
		}

		if tunnel.Type == "" {
			tunnel.Type = "awg"
		}

		// Migration: old tunnels without DefaultRouteSet default to DefaultRoute=true
		if !tunnel.DefaultRouteSet {
			tunnel.DefaultRoute = true
			tunnel.DefaultRouteSet = true
		}

		tunnels = append(tunnels, tunnel)
	}

	return tunnels, nil
}

// Get returns a single tunnel by ID.
func (s *AWGTunnelStore) Get(id string) (*AWGTunnel, error) {
	path := filepath.Join(s.dir, id+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("tunnel not found: %s", id)
		}
		return nil, fmt.Errorf("read tunnel file: %w", err)
	}

	var tunnel AWGTunnel
	if err := json.Unmarshal(data, &tunnel); err != nil {
		return nil, fmt.Errorf("parse tunnel JSON: %w", err)
	}

	if tunnel.Type == "" {
		tunnel.Type = "awg"
	}

	// Migration: old tunnels without DefaultRouteSet default to DefaultRoute=true
	if !tunnel.DefaultRouteSet {
		tunnel.DefaultRoute = true
		tunnel.DefaultRouteSet = true
	}

	return &tunnel, nil
}

// Save writes tunnel to disk.
func (s *AWGTunnelStore) Save(tunnel *AWGTunnel) error {
	lk, err := lock.WaitLockDir(s.lockName, s.lockDir, s.timeout)
	if err != nil {
		return fmt.Errorf("acquire lock: %w", err)
	}
	defer lk.Unlock()

	if tunnel.Type == "" {
		tunnel.Type = "awg"
	}

	// Use Encoder with SetEscapeHTML(false) to preserve < and > in signature fields (I1-I5)
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(tunnel); err != nil {
		return fmt.Errorf("marshal tunnel: %w", err)
	}

	// Remove trailing newline added by Encode
	data := bytes.TrimSuffix(buf.Bytes(), []byte("\n"))

	path := filepath.Join(s.dir, tunnel.ID+".json")
	if err := AtomicWrite(path, data); err != nil {
		return fmt.Errorf("write tunnel file: %w", err)
	}

	return nil
}

// Delete removes tunnel file.
func (s *AWGTunnelStore) Delete(id string) error {
	lk, err := lock.WaitLockDir(s.lockName, s.lockDir, s.timeout)
	if err != nil {
		return fmt.Errorf("acquire lock: %w", err)
	}
	defer lk.Unlock()

	path := filepath.Join(s.dir, id+".json")
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("tunnel not found: %s", id)
		}
		return fmt.Errorf("check tunnel file: %w", err)
	}

	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove tunnel file: %w", err)
	}

	return nil
}

// Exists checks if tunnel exists.
func (s *AWGTunnelStore) Exists(id string) bool {
	path := filepath.Join(s.dir, id+".json")
	_, err := os.Stat(path)
	return err == nil
}

const (
	// OS 5.x: OpkgTun indices 10-16 (NDMS limit is 16)
	os5MinIndex = 10
	os5MaxIndex = 16
)

// NextAvailableID finds the next available tunnel ID.
// - OS 5.x: awg10..awg16 → OpkgTun10..OpkgTun16 (NDMS index limit is 16)
// - OS 4.x: awgm0, awgm1, ... (uses 'm' prefix, no NDMS)
func (s *AWGTunnelStore) NextAvailableID() (string, error) {
	tunnels, err := s.List()
	if err != nil {
		return "", err
	}

	existing := make(map[int]bool)

	if osdetect.Is5() {
		for _, t := range tunnels {
			if len(t.ID) > 3 && t.ID[:3] == "awg" {
				if num, err := strconv.Atoi(t.ID[3:]); err == nil {
					existing[num] = true
				}
			}
		}
		for i := os5MinIndex; i <= os5MaxIndex; i++ {
			if !existing[i] {
				return "awg" + strconv.Itoa(i), nil
			}
		}
		return "", fmt.Errorf("maximum number of tunnels reached (%d)", os5MaxIndex-os5MinIndex+1)
	} else {
		for _, t := range tunnels {
			if len(t.ID) > 4 && t.ID[:4] == "awgm" {
				if num, err := strconv.Atoi(t.ID[4:]); err == nil {
					existing[num] = true
				}
			}
		}
		for i := 0; ; i++ {
			if !existing[i] {
				return "awgm" + strconv.Itoa(i), nil
			}
		}
	}
}
