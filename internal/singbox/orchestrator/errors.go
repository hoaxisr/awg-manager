package orchestrator

import "errors"

// ErrUnknownSlot is returned by Save/SetEnabled when the slot was not
// registered with Register. Producers must register at construction time.
var ErrUnknownSlot = errors.New("orchestrator: unknown slot — register first")

// ErrSlotAlwaysOn is returned by SetEnabled(slot, false) for AlwaysOn
// slots like base.
var ErrSlotAlwaysOn = errors.New("orchestrator: slot is always-on")

// ErrSlotAlreadyRegistered is returned by Register if the slot is
// already known. Producers should call Register at most once per slot.
var ErrSlotAlreadyRegistered = errors.New("orchestrator: slot already registered")

// ErrNoDraft is returned by ApplyDraft when the slot has no pending file
// to apply. HTTP layer maps this to 409 Conflict.
var ErrNoDraft = errors.New("orchestrator: no draft to apply")
