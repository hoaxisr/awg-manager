package orchestrator

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// SaveDraft writes the slot's JSON to pending/<filename> atomically.
// Idempotent overwrite. Does NOT schedule reload — staging is intentionally
// inert until ApplyDraft is called.
//
// Returns ErrUnknownSlot if the slot is not registered.
func (o *Orchestrator) SaveDraft(slot Slot, jsonBytes []byte) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	meta, ok := o.slots[slot]
	if !ok {
		return ErrUnknownSlot
	}
	if err := writeAtomic(o.pendingPath(meta), jsonBytes); err != nil {
		return fmt.Errorf("SaveDraft %s: %w", slot, err)
	}
	return nil
}

// LoadEffective returns pending/<filename> bytes if present, otherwise
// active path bytes. (nil, nil) when neither exists. ErrUnknownSlot if
// the slot is not registered.
//
// Source of truth for "what the user is currently editing": handlers
// reading data for the UI should use this instead of direct file reads.
func (o *Orchestrator) LoadEffective(slot Slot) ([]byte, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	meta, ok := o.slots[slot]
	if !ok {
		return nil, ErrUnknownSlot
	}
	data, err := readIfExists(o.pendingPath(meta))
	if err != nil {
		return nil, fmt.Errorf("LoadEffective pending %s: %w", slot, err)
	}
	if data != nil {
		return data, nil
	}
	data, err = readIfExists(o.activePath(meta))
	if err != nil {
		return nil, fmt.Errorf("LoadEffective active %s: %w", slot, err)
	}
	return data, nil
}

// HasDraft reports whether a pending file exists for the slot.
// Lock-free presence check internally — acquires only briefly to read
// the slot meta map.
func (o *Orchestrator) HasDraft(slot Slot) bool {
	o.mu.Lock()
	meta, ok := o.slots[slot]
	o.mu.Unlock()
	if !ok {
		return false
	}
	_, err := os.Stat(o.pendingPath(meta))
	return err == nil
}

// DiscardDraft removes the pending file for the slot. Idempotent: no
// error if pending was absent. Returns ErrUnknownSlot if slot not
// registered.
func (o *Orchestrator) DiscardDraft(slot Slot) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	meta, ok := o.slots[slot]
	if !ok {
		return ErrUnknownSlot
	}
	return removeIfExists(o.pendingPath(meta))
}

// DraftInfo returns metadata about the pending file for the slot.
// HasDraft false implies DraftedAt zero. ErrUnknownSlot is silently
// translated to a zero DraftInfo (the caller is typically a status
// handler that should not panic on misconfiguration).
func (o *Orchestrator) DraftInfo(slot Slot) DraftInfo {
	o.mu.Lock()
	meta, ok := o.slots[slot]
	o.mu.Unlock()
	if !ok {
		return DraftInfo{}
	}
	st, err := os.Stat(o.pendingPath(meta))
	if err != nil {
		return DraftInfo{}
	}
	return DraftInfo{HasDraft: true, DraftedAt: st.ModTime()}
}

// ApplyDraft validates the pending draft and, if it passes, atomically
// renames pending → active and arms a reload. The validation pipeline
// is: (1) cross-slot validateDraftLocked → (2) sing-box check on a
// tmpdir snapshot mirroring all enabled slots with the target swapped
// for the draft → (3) os.Rename(pending, active).
//
// Returns (ValidationResult, nil) when the logical check fails — the
// caller should surface the errors to the user; pending is preserved
// for further editing.
//
// Returns (ZeroResult, ErrNoDraft) when there is no pending file.
//
// Returns (ZeroResult, wrapped error) on FS or external-check failures.
// In all error paths, pending is preserved and active is unchanged.
func (o *Orchestrator) ApplyDraft(slot Slot) (ValidationResult, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	meta, ok := o.slots[slot]
	if !ok {
		return ValidationResult{}, ErrUnknownSlot
	}
	pendingPath := o.pendingPath(meta)
	draftBytes, err := os.ReadFile(pendingPath)
	if err != nil {
		if os.IsNotExist(err) {
			return ValidationResult{}, ErrNoDraft
		}
		return ValidationResult{}, fmt.Errorf("ApplyDraft read pending %s: %w", slot, err)
	}

	// Cross-slot validation against draft.
	if res := o.validateDraftLocked(slot, draftBytes); !res.Ok() {
		return res, nil
	}

	// sing-box check against tmpdir snapshot.
	if o.validator != nil {
		tmpdir, err := os.MkdirTemp(o.configDir, ".apply-check-*")
		if err != nil {
			return ValidationResult{}, fmt.Errorf("ApplyDraft tmpdir: %w", err)
		}
		defer os.RemoveAll(tmpdir)

		for s, en := range o.enabled {
			if !en {
				continue
			}
			m, ok := o.slots[s]
			if !ok {
				continue
			}
			dst := filepath.Join(tmpdir, m.Filename)
			if s == slot {
				if err := writeAtomic(dst, draftBytes); err != nil {
					return ValidationResult{}, fmt.Errorf("ApplyDraft snapshot draft: %w", err)
				}
				continue
			}
			if err := copyFile(o.activePath(m), dst); err != nil {
				if os.IsNotExist(err) {
					continue // slot enabled but file not yet written — fine
				}
				return ValidationResult{}, fmt.Errorf("ApplyDraft snapshot %s: %w", s, err)
			}
		}

		if err := o.validator.Validate(context.Background(), tmpdir); err != nil {
			return ValidationResult{}, fmt.Errorf("sing-box check: %w", err)
		}
	}

	// Atomic commit.
	if err := os.Rename(pendingPath, o.activePath(meta)); err != nil {
		return ValidationResult{}, fmt.Errorf("ApplyDraft rename: %w", err)
	}
	o.dirty = true
	o.scheduleReload()
	return ValidationResult{}, nil
}

// ValidateDraft is the lock-acquiring public form of validateDraftLocked.
// Used by handlers that want to surface "would this draft apply" in the
// UI without committing.
func (o *Orchestrator) ValidateDraft(slot Slot, draftBytes []byte) ValidationResult {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.validateDraftLocked(slot, draftBytes)
}

// copyFile copies src → dst. dst is overwritten if it exists. Used by
// ApplyDraft to assemble the tmpdir snapshot.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if cErr := out.Close(); err == nil {
		err = cErr
	}
	return err
}
