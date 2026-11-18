package orchestrator

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// appliedStatePath is the tmpfs location where the orchestrator records
// what it last applied to sing-box (config hash + tun presence). A
// package var so tests can redirect it into t.TempDir() — production
// value intentionally lives on tmpfs (/var/run): losing the file across
// a reboot just costs one extra reload on next start, never a
// correctness issue.
var appliedStatePath = "/var/run/awg-manager/singbox-applied.json"

// appliedState is what Reload persists after successfully applying a
// config to sing-box (start / SIGHUP / tun-restart), or clears (zero
// value) after stopping the daemon.
type appliedState struct {
	Hash      string
	HasTun    bool
	PID       int    `json:"pid,omitempty"`
	ProcToken string `json:"procToken,omitempty"`
}

// clearAppliedState removes the persisted applied-state breadcrumb so a stopped
// process generation cannot later be falsely adopted across restart/park cycles.
func clearAppliedState(path string) error {
	if path == "" {
		return nil
	}
	err := os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// readProcStartToken returns an OS-level process generation token ("<pid>:<starttime>")
// from /proc/<pid>/stat on Linux so PID reuse or external respawns cannot match
// a stale singbox-applied.json breadcrumb. Returns "" when /proc is unavailable
// or in mock/test environments without real PIDs.
func readProcStartToken(pid int) string {
	if pid <= 0 {
		return ""
	}
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return ""
	}
	line := string(data)
	idx := strings.LastIndexByte(line, ')')
	if idx < 0 || idx+1 >= len(line) {
		return ""
	}
	fields := strings.Fields(line[idx+1:])
	// After ") ", field index 0 in fields is proc field 3 (state); field 22 (starttime) is index 19.
	if len(fields) <= 19 || fields[19] == "" {
		return ""
	}
	return fmt.Sprintf("%d:%s", pid, fields[19])
}

// loadAppliedState reads the persisted applied-state from path. ok is
// false whenever the file is missing or unparseable — both are treated
// as "no history" (first boot, tmpfs wipe, or a stray write failure),
// never as a hard error.
func loadAppliedState(path string) (appliedState, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return appliedState{}, false
	}
	var st appliedState
	if json.Unmarshal(data, &st) != nil {
		return appliedState{}, false
	}
	return st, true
}

// saveAppliedState persists st to path. Not atomic on purpose — this is
// a tmpfs breadcrumb, not durable config: a torn write just means the
// next Reload doesn't skip (fail-safe, never fail-dangerous).
func saveAppliedState(path string, st appliedState) error {
	data, err := json.Marshal(st)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

// enabledConfigHashLocked hashes the ACTIVE bytes of every ENABLED slot,
// in the fixed KnownSlots() catalog order (map iteration order is
// random, so it cannot be used here). Reload compares this against the
// hash it saved after the last successful apply to detect "config
// unchanged since we last touched sing-box" — the signal that lets a
// daemon restart adopt an already-running sing-box without a pointless
// SIGHUP or, worse, a tun-restart. Caller MUST hold o.mu.
func (o *Orchestrator) enabledConfigHashLocked() string {
	h := sha256.New()
	for _, meta := range KnownSlots() {
		if _, ok := o.slots[meta.Slot]; !ok || !o.enabled[meta.Slot] {
			continue
		}
		data, err := o.readActiveBytes(meta.Slot)
		if err != nil {
			continue
		}
		h.Write([]byte(meta.Slot))
		h.Write([]byte{0})
		h.Write(data)
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}
