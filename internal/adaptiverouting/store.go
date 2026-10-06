package adaptiverouting

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type Store struct {
	dataDir       string
	settingsPath  string
	statePath     string
	mu            sync.RWMutex
	settings      Settings
	settingsMtime time.Time
	settingsSize  int64
	applied       *AppliedConfig
	state         OperationalState
}

const runtimeDocumentVersion = 1

type runtimeDocument struct {
	Version int              `json:"version"`
	Applied *AppliedConfig   `json:"applied,omitempty"`
	State   OperationalState `json:"state"`
}

func NewStore(dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return nil, fmt.Errorf("adaptive routing store: create dir %s: %w", dataDir, err)
	}
	s := &Store{
		dataDir:      dataDir,
		settingsPath: filepath.Join(dataDir, "susanin.json"),
		statePath:    filepath.Join(dataDir, "susanin_state.json"),
		settings:     DefaultSettings(),
		state: OperationalState{
			RoutingOwner: RoutingOwnerNone,
			Status:       "stopped",
		},
	}

	if err := s.loadSettings(); err != nil {
		return nil, err
	}
	if err := s.loadState(); err != nil {
		return nil, err
	}

	return s, nil
}

func (s *Store) loadSettings() error {
	b, err := os.ReadFile(s.settingsPath)
	if errors.Is(err, os.ErrNotExist) {
		return s.saveSettingsLocked()
	}
	if err != nil {
		return fmt.Errorf("adaptive routing store: read settings: %w", err)
	}
	if len(b) == 0 {
		return s.saveSettingsLocked()
	}
	var loaded Settings
	if err := json.Unmarshal(b, &loaded); err != nil {
		return fmt.Errorf("adaptive routing store: parse settings: %w", err)
	}

	// Apply defaults if fields are missing
	if loaded.RoutingTableID <= 0 {
		loaded.RoutingTableID = 105
	}
	if loaded.FwmarkMask == "" {
		loaded.FwmarkMask = "0x30000000"
	}
	if loaded.FwmarkTest == "" {
		loaded.FwmarkTest = "0x10000000"
	}
	if loaded.FwmarkOk == "" {
		loaded.FwmarkOk = "0x20000000"
	}
	if loaded.RulePriorityTest <= 0 {
		loaded.RulePriorityTest = 96
	}
	if loaded.RulePriorityOk <= 0 {
		loaded.RulePriorityOk = 95
	}
	if loaded.FailurePolicy == "" {
		loaded.FailurePolicy = "direct"
	}
	if loaded.Detection.FastIntervalSeconds <= 0 {
		loaded.Detection.FastIntervalSeconds = 1
	}
	if loaded.Detection.SoftIntervalSeconds <= 0 {
		loaded.Detection.SoftIntervalSeconds = 1
	}
	if loaded.Detection.JudgeIntervalSeconds <= 0 {
		loaded.Detection.JudgeIntervalSeconds = 1
	}
	if loaded.Detection.HealthIntervalSeconds <= 0 {
		loaded.Detection.HealthIntervalSeconds = 5
	}
	if loaded.Detection.LateStallBytes <= 0 {
		loaded.Detection.LateStallBytes = 65536
	}
	if loaded.Persistence.MaxEntries <= 0 {
		loaded.Persistence.MaxEntries = 4096
	}
	if len(loaded.DNS.Servers) == 0 {
		loaded.DNS.Servers = []string{"1.1.1.1", "8.8.8.8"}
	}

	if fi, err := os.Stat(s.settingsPath); err == nil {
		s.settingsMtime = fi.ModTime()
		s.settingsSize = fi.Size()
	}
	s.settings = loaded
	return nil
}

func (s *Store) loadState() error {
	b, err := os.ReadFile(s.statePath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("adaptive routing store: read state: %w", err)
	}
	if len(b) == 0 {
		return nil
	}
	var doc runtimeDocument
	if err := json.Unmarshal(b, &doc); err == nil && doc.Version > 0 {
		s.applied = s.cloneApplied(doc.Applied)
		s.state = doc.State
		return nil
	}

	// Backward compatibility with the pre-transactional state file. A legacy
	// state is diagnostic only: it must never be treated as an applied config,
	// because the old format did not contain enough data for safe restoration.
	var legacy OperationalState
	if err := json.Unmarshal(b, &legacy); err != nil {
		return fmt.Errorf("adaptive routing store: parse state: %w", err)
	}
	s.state = legacy
	s.applied = nil
	return nil
}

func (s *Store) reloadSettingsIfChangedLocked() {
	fi, err := os.Stat(s.settingsPath)
	if err != nil {
		return
	}
	if !fi.ModTime().Equal(s.settingsMtime) || fi.Size() != s.settingsSize {
		_ = s.loadSettings()
	}
}

func (s *Store) GetSettings() Settings {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reloadSettingsIfChangedLocked()
	return s.cloneSettings(s.settings)
}

func (s *Store) UpdateSettings(fn func(*Settings) error) (Settings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.reloadSettingsIfChangedLocked()
	candidate := s.cloneSettings(s.settings)
	if err := fn(&candidate); err != nil {
		return Settings{}, err
	}

	if err := s.saveSettingsValueLocked(candidate); err != nil {
		return Settings{}, err
	}
	s.settings = candidate
	return s.cloneSettings(s.settings), nil
}

// GetApplied returns the last configuration that was committed only after all
// runtime stages succeeded. A nil result means that there is no safe config to
// restore automatically.
func (s *Store) GetApplied() *AppliedConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cloneApplied(s.applied)
}

func (s *Store) GetState() OperationalState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cloneState(s.state)
}

func (s *Store) UpdateState(fn func(*OperationalState)) OperationalState {
	state, _ := s.SaveState(func(st *OperationalState) error {
		fn(st)
		return nil
	})
	return state
}

// SaveState persists runtime state together with the applied config. Memory is
// changed only after the atomic file replacement succeeds.
func (s *Store) SaveState(fn func(*OperationalState) error) (OperationalState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	candidate := s.cloneState(s.state)
	if err := fn(&candidate); err != nil {
		return s.cloneState(s.state), err
	}
	if err := s.saveRuntimeLocked(s.applied, candidate); err != nil {
		return s.cloneState(s.state), err
	}
	s.state = candidate
	return s.cloneState(s.state), nil
}

// CommitApplied atomically advances both the applied configuration and its
// operational state. Callers must invoke it only after executor, datapath and
// process readiness checks have all succeeded.
func (s *Store) CommitApplied(applied AppliedConfig, state OperationalState) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	candidate := applied
	if err := s.saveRuntimeLocked(&candidate, state); err != nil {
		return err
	}
	s.applied = s.cloneApplied(&candidate)
	s.state = s.cloneState(state)
	return nil
}

// ClearApplied records a fully stopped runtime. The desired draft is kept so
// the UI can show the user's last choices, but boot will not auto-start it.
func (s *Store) ClearApplied(state OperationalState) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.saveRuntimeLocked(nil, state); err != nil {
		return err
	}
	s.applied = nil
	s.state = s.cloneState(state)
	return nil
}

func (s *Store) saveSettingsLocked() error {
	return s.saveSettingsValueLocked(s.settings)
}

func (s *Store) saveSettingsValueLocked(settings Settings) error {
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("adaptive routing store: marshal settings: %w", err)
	}
	if err := atomicWrite(s.settingsPath, data); err != nil {
		return err
	}
	if fi, err := os.Stat(s.settingsPath); err == nil {
		s.settingsMtime = fi.ModTime()
		s.settingsSize = fi.Size()
	}
	return nil
}

func (s *Store) saveStateLocked() error {
	return s.saveRuntimeLocked(s.applied, s.state)
}

func (s *Store) saveRuntimeLocked(applied *AppliedConfig, state OperationalState) error {
	doc := runtimeDocument{
		Version: runtimeDocumentVersion,
		Applied: s.cloneApplied(applied),
		State:   s.cloneState(state),
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("adaptive routing store: marshal state: %w", err)
	}
	return atomicWrite(s.statePath, data)
}

func atomicWrite(dest string, data []byte) error {
	dir := filepath.Dir(dest)
	tmpFile, err := os.CreateTemp(dir, "susanin-tmp-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmpFile.Name()
	defer func() {
		_ = tmpFile.Close()
		_ = os.Remove(tmpName)
	}()

	if _, err := tmpFile.Write(data); err != nil {
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmpFile.Sync(); err != nil {
		return fmt.Errorf("sync temp file: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}

	if err := os.Rename(tmpName, dest); err != nil {
		return fmt.Errorf("rename to %s: %w", dest, err)
	}
	return nil
}

func (s *Store) cloneSettings(in Settings) Settings {
	cp := in
	cp.FallbackEgresses = append([]EgressRef(nil), in.FallbackEgresses...)
	cp.Source.Interfaces = append([]string(nil), in.Source.Interfaces...)
	cp.AlwaysEntries = append([]string(nil), in.AlwaysEntries...)
	cp.NeverEntries = append([]string(nil), in.NeverEntries...)
	return cp
}

func (s *Store) cloneState(in OperationalState) OperationalState {
	cp := in
	if in.ActiveEgress != nil {
		eg := *in.ActiveEgress
		cp.ActiveEgress = &eg
	}
	return cp
}

func (s *Store) cloneApplied(in *AppliedConfig) *AppliedConfig {
	if in == nil {
		return nil
	}
	cp := *in
	cp.Settings = s.cloneSettings(in.Settings)
	cp.Egress = in.Egress
	return &cp
}
