package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/hoaxisr/awg-manager/awgmautolist/internal/model"
)

var knownFields = []string{
	"autoAdd", "targetInterface", "resolveVia", "wanCheckUrl",
	"wanMonitorEnabled", "tunnelProbe", "udpEnabled", "verbose", "addsPerHour",
	"excludeDomains", "excludeRuleSets",
}

// Store serializes read-modify-write operations. fields preserves unknown JSON
// members so a newer/older writer does not erase settings it does not know.
type Store struct {
	mu     sync.Mutex
	path   string
	cfg    Config
	fields map[string]json.RawMessage
}

type LoadReport struct {
	Warnings []string
}

func NewStore(path string) *Store {
	return &Store{path: path, cfg: Default(), fields: map[string]json.RawMessage{}}
}

func (s *Store) Config() Config {
	s.mu.Lock()
	defer s.mu.Unlock()
	return clone(s.cfg)
}

// Load reads settings without writing a migration to flash. A migrated value is
// persisted only on a subsequent explicit Patch.
func (s *Store) Load() (LoadReport, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		s.cfg = Default()
		s.fields = map[string]json.RawMessage{}
		return LoadReport{}, nil
	}
	if err != nil {
		return LoadReport{}, err
	}
	fields, err := decodeObject(data)
	if err != nil {
		return LoadReport{}, err
	}
	cfg, err := decodeKnown(fields, Default())
	if err != nil {
		return LoadReport{}, err
	}
	report := LoadReport{}
	if cfg.ResolveVia != "" && !strings.HasPrefix(cfg.ResolveVia, "sb:") {
		report.Warnings = append(report.Warnings,
			fmt.Sprintf("legacy resolveVia=%q replaced with sing-box dns.final", cfg.ResolveVia))
		cfg.ResolveVia = ""
	}
	if !cfg.TunnelProbe && cfg.AutoAdd {
		report.Warnings = append(report.Warnings, "autoAdd disabled because tunnelProbe is off")
	}
	if err := validate(&cfg, false); err != nil {
		return LoadReport{}, err
	}
	normalized, err := normalizedFields(fields, cfg)
	if err != nil {
		return LoadReport{}, err
	}
	s.cfg = clone(cfg)
	s.fields = normalized
	return report, nil
}

// Patch applies a JSON merge patch, validates it and atomically persists the
// result before publishing it to in-memory readers.
func (s *Store) Patch(data []byte) (Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	patch, err := decodeObject(data)
	if err != nil {
		return Config{}, err
	}
	fields := cloneFields(s.fields)
	for key, value := range patch {
		fields[key] = value
	}
	cfg, err := decodeKnown(fields, s.cfg)
	if err != nil {
		return Config{}, err
	}
	_, autoExplicit := patch["autoAdd"]
	if err := validate(&cfg, autoExplicit); err != nil {
		return Config{}, err
	}
	encoded, err := encodeWithUnknown(fields, cfg)
	if err != nil {
		return Config{}, err
	}
	if err := writeAtomic(s.path, encoded); err != nil {
		return Config{}, err
	}
	storedFields, err := decodeObject(encoded)
	if err != nil {
		return Config{}, err
	}
	s.cfg = clone(cfg)
	s.fields = storedFields
	return clone(cfg), nil
}

func decodeObject(data []byte) (map[string]json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, fmt.Errorf("config must be a JSON object: %w", err)
	}
	if fields == nil {
		return nil, errors.New("config must be a non-null JSON object")
	}
	return fields, nil
}

func decodeKnown(fields map[string]json.RawMessage, base Config) (Config, error) {
	for _, key := range knownFields {
		if raw, ok := fields[key]; ok && string(raw) == "null" {
			return Config{}, fmt.Errorf("%s must not be null", key)
		}
	}
	data, err := json.Marshal(fields)
	if err != nil {
		return Config{}, err
	}
	cfg := clone(base)
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("invalid config field: %w", err)
	}
	return cfg, nil
}

func validate(cfg *Config, autoExplicit bool) error {
	if cfg.AddsPerHour < 1 || cfg.AddsPerHour > MaxAddsPerHour {
		return fmt.Errorf("addsPerHour must be between 1 and %d", MaxAddsPerHour)
	}
	cfg.TargetInterface = strings.TrimSpace(cfg.TargetInterface)
	if strings.ContainsAny(cfg.TargetInterface, " \t\r\n") {
		return errors.New("targetInterface must be a kernel interface name")
	}
	cfg.ResolveVia = strings.TrimSpace(cfg.ResolveVia)
	if cfg.ResolveVia != "" {
		tag := strings.TrimPrefix(cfg.ResolveVia, "sb:")
		if tag == cfg.ResolveVia || tag == "" || strings.ContainsAny(tag, " \t\r\n/") {
			return errors.New("resolveVia must be empty or sb:<tag>")
		}
	}
	cfg.WANCheckURL = strings.TrimSpace(cfg.WANCheckURL)
	if cfg.WANCheckURL == "" {
		cfg.WANCheckURL = DefaultWANCheckURL
	}
	u, err := url.Parse(cfg.WANCheckURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
		return errors.New("wanCheckUrl must be a plain http(s) URL")
	}
	if cfg.AutoAdd && !cfg.TunnelProbe {
		if autoExplicit {
			return errors.New("autoAdd requires tunnelProbe")
		}
		cfg.AutoAdd = false
	}
	cfg.ExcludeDomains, err = normalizeDomains(cfg.ExcludeDomains)
	if err != nil {
		return err
	}
	cfg.ExcludeRuleSets, err = normalizeTags(cfg.ExcludeRuleSets)
	return err
}

func normalizeDomains(input []string) ([]string, error) {
	seen := map[string]bool{}
	out := make([]string, 0, len(input))
	for _, raw := range input {
		name, err := model.NormalizeDomain(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid excluded domain %q", raw)
		}
		if !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	return out, nil
}

func normalizeTags(input []string) ([]string, error) {
	seen := map[string]bool{}
	out := make([]string, 0, len(input))
	for _, raw := range input {
		tag := strings.TrimSpace(raw)
		if tag == "" || strings.ContainsAny(tag, " \t\r\n/") {
			return nil, fmt.Errorf("invalid rule-set tag %q", raw)
		}
		if !seen[tag] {
			seen[tag] = true
			out = append(out, tag)
		}
	}
	return out, nil
}

func encodeWithUnknown(fields map[string]json.RawMessage, cfg Config) ([]byte, error) {
	out := cloneFields(fields)
	for _, key := range knownFields {
		delete(out, key)
	}
	known, err := json.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	var encoded map[string]json.RawMessage
	if err := json.Unmarshal(known, &encoded); err != nil {
		return nil, err
	}
	for key, value := range encoded {
		out[key] = value
	}
	return json.MarshalIndent(out, "", "  ")
}

func normalizedFields(fields map[string]json.RawMessage, cfg Config) (map[string]json.RawMessage, error) {
	data, err := encodeWithUnknown(fields, cfg)
	if err != nil {
		return nil, err
	}
	return decodeObject(data)
}

func cloneFields(input map[string]json.RawMessage) map[string]json.RawMessage {
	out := make(map[string]json.RawMessage, len(input))
	for key, value := range input {
		out[key] = append(json.RawMessage(nil), value...)
	}
	return out
}

func writeAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".awgmautolist-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}
