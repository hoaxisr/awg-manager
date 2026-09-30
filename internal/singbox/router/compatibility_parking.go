package router

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/strictfs"
)

const (
	// CompatibilityParkingStateVersion is the current schema version for parking state.
	CompatibilityParkingStateVersion uint32 = 1

	// CompatibilityParkingFileName is the filename under orchestrator configDir.
	CompatibilityParkingFileName = "compatibility_parking.state"

	// CompatibilityParkingOwner identifies records managed by this reconcile loop.
	CompatibilityParkingOwner = "mihomo_engine_reconcile"

	// CompatibilityParkingReasonConflict records port conflict as the reason for parking.
	CompatibilityParkingReasonConflict = "port_conflict"

	// DefaultMihomoMixedPort is the default mixed port used by Mihomo when unspecified.
	DefaultMihomoMixedPort = 1099
)

// compatibilityParkingState represents the persisted versioned document of parked slots.
type compatibilityParkingState struct {
	Version uint32                                `json:"version"`
	Records map[string]compatibilityParkingRecord `json:"records"`
}

// compatibilityParkingRecord tracks an individual parked slot.
type compatibilityParkingRecord struct {
	Slot            orchestrator.Slot `json:"slot"`
	PreviousEnabled bool              `json:"previous_enabled"`
	Reason          string            `json:"reason"`
	Owner           string            `json:"owner"`
	ParkedAt        time.Time         `json:"parked_at"`
	ConfigDigest    string            `json:"config_digest"`
	ConflictPort    int               `json:"conflict_port"`
}

// compatibilityFragment decodes applied DeviceProxy inbound configurations.
type compatibilityFragment struct {
	Inbounds []struct {
		Type       string `json:"type"`
		ListenPort int    `json:"listen_port"`
	} `json:"inbounds"`
}

// effectiveMihomoMixedPort resolves the mixed port for conflict inspection.
func (s *ServiceImpl) effectiveMihomoMixedPort(sr storage.SingboxRouterSettings) int {
	if sr.MihomoMixedPort > 0 {
		return sr.MihomoMixedPort
	}
	return DefaultMihomoMixedPort
}

// checkJSONDuplicateKeys validates that JSON bytes do not contain duplicate keys.
func checkJSONDuplicateKeys(dec *json.Decoder) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		return nil
	}
	if delim == '{' {
		keys := make(map[string]struct{})
		for dec.More() {
			keyTok, err := dec.Token()
			if err != nil {
				return err
			}
			key, ok := keyTok.(string)
			if !ok {
				return fmt.Errorf("expected string key in JSON object")
			}
			if _, exists := keys[key]; exists {
				return fmt.Errorf("duplicate key %q in JSON object", key)
			}
			keys[key] = struct{}{}
			if err := checkJSONDuplicateKeys(dec); err != nil {
				return err
			}
		}
		_, err := dec.Token() // consume '}'
		return err
	} else if delim == '[' {
		for dec.More() {
			if err := checkJSONDuplicateKeys(dec); err != nil {
				return err
			}
		}
		_, err := dec.Token() // consume ']'
		return err
	}
	return nil
}

// loadCompatibilityParkingStateLocked reads the versioned parking document from disk.
// Caller MUST hold lifecycle lock (s.mu).
func (s *ServiceImpl) loadCompatibilityParkingStateLocked() (*compatibilityParkingState, error) {
	if s.deps.Orch == nil {
		return nil, errors.New("orchestrator dependency is nil")
	}
	path := filepath.Join(s.deps.Orch.ConfigDir(), CompatibilityParkingFileName)
	fi, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &compatibilityParkingState{
				Version: CompatibilityParkingStateVersion,
				Records: make(map[string]compatibilityParkingRecord),
			}, nil
		}
		return nil, fmt.Errorf("stat parking state file: %w", err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("parking state file %s is a symlink", path)
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("parking state file %s is not a regular file", path)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read parking state file: %w", err)
	}

	// Strict JSON validation: reject duplicate keys and trailing data
	decCheck := json.NewDecoder(bytes.NewReader(data))
	if err := checkJSONDuplicateKeys(decCheck); err != nil {
		return nil, fmt.Errorf("duplicate key in parking state: %w", err)
	}
	var trailingCheck struct{}
	if err := decCheck.Decode(&trailingCheck); err != io.EOF {
		return nil, fmt.Errorf("trailing data in parking state")
	}

	var state compatibilityParkingState
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&state); err != nil {
		return nil, fmt.Errorf("decode parking state: %w", err)
	}
	if dec.More() {
		return nil, fmt.Errorf("trailing data in parking state")
	}

	if err := validateCompatibilityParkingState(&state); err != nil {
		return nil, fmt.Errorf("validate parking state: %w", err)
	}
	return &state, nil
}

// isValidLowerHex checks if a string consists exclusively of lowercase hex characters [0-9a-f].
func isValidLowerHex(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

// validateCompatibilityParkingState performs strict semantic validation of the entire state document.
func validateCompatibilityParkingState(state *compatibilityParkingState) error {
	if state == nil {
		return errors.New("nil compatibility parking state")
	}
	if state.Version != CompatibilityParkingStateVersion {
		return fmt.Errorf("unsupported parking state version %d (expected %d)", state.Version, CompatibilityParkingStateVersion)
	}
	if state.Records == nil {
		return errors.New("parking state records map is nil")
	}
	for key, rec := range state.Records {
		if key != string(rec.Slot) {
			return fmt.Errorf("map key %q does not match record slot %q", key, rec.Slot)
		}
		if rec.Slot != orchestrator.SlotDeviceProxy {
			return fmt.Errorf("unsupported slot %q (expected %q)", rec.Slot, orchestrator.SlotDeviceProxy)
		}
		if rec.Owner != CompatibilityParkingOwner {
			return fmt.Errorf("unsupported record owner %q for slot %s", rec.Owner, key)
		}
		if rec.Reason != CompatibilityParkingReasonConflict {
			return fmt.Errorf("invalid parking reason %q for slot %s (expected %q)", rec.Reason, key, CompatibilityParkingReasonConflict)
		}
		if !rec.PreviousEnabled {
			return fmt.Errorf("invalid previous_enabled for slot %s: must be true", key)
		}
		if len(rec.ConfigDigest) != 64 || !isValidLowerHex(rec.ConfigDigest) {
			return fmt.Errorf("invalid config_digest %q for slot %s: must be 64-character lowercase hex SHA-256 digest", rec.ConfigDigest, key)
		}
		if rec.ConflictPort <= 0 || rec.ConflictPort > 65535 {
			return fmt.Errorf("invalid conflict_port %d for slot %s: must be between 1 and 65535", rec.ConflictPort, key)
		}
		if rec.ParkedAt.IsZero() {
			return fmt.Errorf("invalid parked_at for slot %s: timestamp must be non-zero", key)
		}
	}
	return nil
}

// saveCompatibilityParkingStateLocked writes the parking state to disk atomically.
// Caller MUST hold lifecycle lock (s.mu).
func (s *ServiceImpl) saveCompatibilityParkingStateLocked(state *compatibilityParkingState) error {
	if s.deps.Orch == nil {
		return errors.New("orchestrator dependency is nil")
	}
	if state == nil {
		return errors.New("nil compatibility parking state")
	}
	if err := validateCompatibilityParkingState(state); err != nil {
		return fmt.Errorf("validate parking state before save: %w", err)
	}

	path := filepath.Join(s.deps.Orch.ConfigDir(), CompatibilityParkingFileName)
	if fi, err := os.Lstat(path); err == nil {
		if fi.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("cannot overwrite symlink %s", path)
		}
		if !fi.Mode().IsRegular() {
			return fmt.Errorf("cannot overwrite non-regular file %s", path)
		}
	}

	if len(state.Records) == 0 {
		return strictfs.StrictUnlink(path)
	}

	state.Version = CompatibilityParkingStateVersion
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal parking state: %w", err)
	}
	return strictfs.StrictWriteAtomic(path, data, 0o600)
}

// checkDeviceProxyPortConflict inspects applied DeviceProxy inbound configuration for port collision.
func (s *ServiceImpl) checkDeviceProxyPortConflict(sr storage.SingboxRouterSettings) (bool, int, []byte, error) {
	effectivePort := s.effectiveMihomoMixedPort(sr)
	data, err := s.deps.Orch.LoadApplied(orchestrator.SlotDeviceProxy)
	if err != nil {
		if errors.Is(err, orchestrator.ErrUnknownSlot) {
			return false, effectivePort, nil, nil
		}
		return false, effectivePort, nil, fmt.Errorf("load applied deviceproxy: %w", err)
	}
	if len(data) == 0 {
		return false, effectivePort, nil, nil
	}

	// Strict JSON validation: reject duplicate keys and trailing data
	decCheck := json.NewDecoder(bytes.NewReader(data))
	if err := checkJSONDuplicateKeys(decCheck); err != nil {
		return false, effectivePort, data, fmt.Errorf("duplicate key in deviceproxy config: %w", err)
	}
	var trailingCheck struct{}
	if err := decCheck.Decode(&trailingCheck); err != io.EOF {
		return false, effectivePort, data, fmt.Errorf("trailing data in deviceproxy config")
	}

	var frag compatibilityFragment
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&frag); err != nil {
		return false, effectivePort, data, fmt.Errorf("decode deviceproxy config: %w", err)
	}
	if dec.More() {
		return false, effectivePort, data, fmt.Errorf("trailing data in deviceproxy config")
	}

	for _, ib := range frag.Inbounds {
		if ib.ListenPort == effectivePort {
			return true, effectivePort, data, nil
		}
	}
	return false, effectivePort, data, nil
}
