package orchestrator

import (
	"fmt"
	"os"
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
