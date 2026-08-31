package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"time"
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

// LoadEffective returns the bytes of the "most relevant" copy of a slot
// for UI consumers. Priority chain:
//
//  1. pending/<filename>  — user's in-flight edits (SaveDraft target)
//  2. active/<filename>   — applied config, slot enabled
//  3. disabled/<filename> — saved config, slot disabled
//  4. (nil, nil)          — slot never configured
//
// Including disabled/ makes "engine off" mean "inactive but editable":
// UI handlers (ListRules etc.) keep showing the user's rules so they can
// be reviewed and re-enabled without re-entering. Returns ErrUnknownSlot
// when the slot is not registered.
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
	if data != nil {
		return data, nil
	}
	data, err = readIfExists(o.disabledPath(meta))
	if err != nil {
		return nil, fmt.Errorf("LoadEffective disabled %s: %w", slot, err)
	}
	return data, nil
}

// LoadDraft returns the bytes of the slot's pending (draft) file, or
// (nil, nil) when no draft exists. Unlike LoadEffective it never falls
// back to active/disabled — callers that validate "the current draft"
// (config-editor check endpoint) must not silently re-check the applied
// config instead. Returns ErrUnknownSlot for unregistered slots.
func (o *Orchestrator) LoadDraft(slot Slot) ([]byte, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	meta, ok := o.slots[slot]
	if !ok {
		return nil, ErrUnknownSlot
	}
	data, err := readIfExists(o.pendingPath(meta))
	if err != nil {
		return nil, fmt.Errorf("LoadDraft pending %s: %w", slot, err)
	}
	return data, nil
}

// EffectiveStat returns size and mtime of the file LoadEffective would
// read (same pending → active → disabled priority chain). exists=false
// when the slot has no file anywhere. Used by the config-editor slots
// list, which needs metadata without pulling file contents.
func (o *Orchestrator) EffectiveStat(slot Slot) (size int64, mtime time.Time, exists bool) {
	o.mu.Lock()
	meta, ok := o.slots[slot]
	o.mu.Unlock()
	if !ok {
		return 0, time.Time{}, false
	}
	for _, p := range []string{o.pendingPath(meta), o.activePath(meta), o.disabledPath(meta)} {
		st, err := os.Stat(p)
		if err == nil && st.Mode().IsRegular() {
			return st.Size(), st.ModTime(), true
		}
	}
	return 0, time.Time{}, false
}

// LoadApplied returns the bytes of the slot's APPLIED config: active/ when
// the slot is enabled, otherwise disabled/. Unlike LoadEffective it never
// reads pending/ — callers that make enforcement decisions (e.g. the
// reconcile self-heal) must not act on an un-applied
// draft the user may still discard. Returns (nil, nil) when the slot was
// never configured.
func (o *Orchestrator) LoadApplied(slot Slot) ([]byte, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	meta, ok := o.slots[slot]
	if !ok {
		return nil, ErrUnknownSlot
	}
	data, err := readIfExists(o.activePath(meta))
	if err != nil {
		return nil, fmt.Errorf("LoadApplied active %s: %w", slot, err)
	}
	if data != nil {
		return data, nil
	}
	data, err = readIfExists(o.disabledPath(meta))
	if err != nil {
		return nil, fmt.Errorf("LoadApplied disabled %s: %w", slot, err)
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
// for further editing. On SUCCESS the returned result may still carry
// advisory entries (SeverityWarning, e.g. route-final-conflict) — callers
// surface them without blocking.
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

	// Cross-slot validation against draft. Результат сохраняем: при успехе
	// он может нести advisory-предупреждения, которые нужно донести до UI.
	res := o.validateDraftLocked(slot, draftBytes)
	if !res.Ok() {
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
			if !en || s == slot {
				continue
			}
			m, ok := o.slots[s]
			if !ok {
				continue
			}
			dst := filepath.Join(tmpdir, m.Filename)
			if err := copyFile(o.activePath(m), dst); err != nil {
				if os.IsNotExist(err) {
					continue // slot enabled but file not yet written — fine
				}
				return ValidationResult{}, fmt.Errorf("ApplyDraft snapshot %s: %w", s, err)
			}
		}
		// Черновик цели пишется в снапшот БЕЗУСЛОВНО, даже если слот сейчас
		// выключен: цель валидируется «как будто применена и включена» — тот
		// же контракт, что у CheckMerged/validateWithEnabled (зеркалит
		// checkMergedLocked). Иначе черновик выключенного слота исключался бы
		// из `sing-box check`, хотя validateDraftLocked уже считает его
		// включённым — внутренне противоречиво и пропускало бы ошибки.
		if err := writeAtomic(filepath.Join(tmpdir, meta.Filename), draftBytes); err != nil {
			return ValidationResult{}, fmt.Errorf("ApplyDraft snapshot draft: %w", err)
		}

		if err := o.validator.Validate(context.Background(), tmpdir); err != nil {
			return ValidationResult{}, fmt.Errorf("sing-box check: %w", err)
		}
	}

	// Atomic commit.
	if err := os.Rename(pendingPath, o.activePath(meta)); err != nil {
		return ValidationResult{}, fmt.Errorf("ApplyDraft rename: %w", err)
	}
	o.scheduleReload()
	return res, nil // res.Ok() == true; может содержать advisory-предупреждения
}

// CheckMerged runs the full validation pipeline (cross-slot logical
// check + `sing-box check` over a tmpdir snapshot of every enabled slot
// with the target slot overlaid by jsonBytes) WITHOUT writing anything.
//
// Used by callers that need iterative validation — e.g. the subscription
// adapter dropping one bad outbound at a time after parsing the
// `outbound[INDEX]` from sing-box's error — and don't want a partial
// write between iterations.
//
// The target slot is included in the snapshot regardless of its current
// o.enabled value: "validate as if applied", not "validate against
// currently-active siblings only". ApplyDraft mirrors the same contract.
func (o *Orchestrator) CheckMerged(slot Slot, jsonBytes []byte) (ValidationResult, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.checkMergedLocked(slot, jsonBytes)
}

// CheckSlotAlone runs `sing-box check` over a tmpdir containing ONLY the
// given slot's candidate bytes — no sibling slots, no cross-slot logical
// validation. Intra-slot decode/initialize errors surface with the same
// outbound-index attribution as CheckMerged, at a fraction of the cost:
// no snapshot of every enabled slot and no parse of their contents per
// invocation.
//
// Built for the subscription adapter's iterative drop loop (issue #491):
// public share-link lists routinely carry dozens of entries sing-box
// rejects, and the loop spawns one check per rejected entry — with
// CheckMerged each spawn also re-parsed every other slot, which on a
// MIPS/ARM router turned a large-subscription Create into minutes of
// silent spinner. Cross-slot classes (duplicate tags vs 90-user, dangling
// group refs) are NOT visible here — callers must finish with one
// CheckMerged.
func (o *Orchestrator) CheckSlotAlone(slot Slot, jsonBytes []byte) (ValidationResult, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	meta, ok := o.slots[slot]
	if !ok {
		return ValidationResult{}, ErrUnknownSlot
	}
	res := ValidationResult{}
	if o.validator == nil {
		return res, nil
	}
	tmpdir, err := os.MkdirTemp(o.configDir, ".alone-check-*")
	if err != nil {
		return ValidationResult{}, fmt.Errorf("CheckSlotAlone tmpdir: %w", err)
	}
	defer os.RemoveAll(tmpdir)
	if err := writeAtomic(filepath.Join(tmpdir, meta.Filename), jsonBytes); err != nil {
		return ValidationResult{}, fmt.Errorf("CheckSlotAlone write target: %w", err)
	}
	if err := o.validator.Validate(context.Background(), tmpdir); err != nil {
		ve := ValidationError{
			Slot:    slot,
			Kind:    "sing-box check",
			Message: err.Error(),
		}
		if s, idx, ok := o.attributeOutboundIndex(err.Error(), tmpdir); ok {
			ve.OutboundSlot, ve.OutboundIndex = s, &idx
		}
		res.Errors = append(res.Errors, ve)
	}
	return res, nil
}

// checkMergedLocked is the shared body for SaveAndValidate / CheckMerged.
// Caller MUST hold o.mu.
func (o *Orchestrator) checkMergedLocked(slot Slot, jsonBytes []byte) (ValidationResult, error) {
	meta, ok := o.slots[slot]
	if !ok {
		return ValidationResult{}, ErrUnknownSlot
	}

	// Логический результат сохраняем целиком: при Ok() он может нести
	// advisory-предупреждения (SeverityWarning), которые возвращаются
	// вызывающему вместе с успехом — иначе UI их никогда не увидит.
	res := o.validateDraftLocked(slot, jsonBytes)
	if !res.Ok() {
		return res, nil
	}

	if o.validator == nil {
		return res, nil
	}

	tmpdir, err := os.MkdirTemp(o.configDir, ".save-check-*")
	if err != nil {
		return ValidationResult{}, fmt.Errorf("CheckMerged tmpdir: %w", err)
	}
	defer os.RemoveAll(tmpdir)

	for s, en := range o.enabled {
		if !en || s == slot {
			continue
		}
		m, ok := o.slots[s]
		if !ok {
			continue
		}
		dst := filepath.Join(tmpdir, m.Filename)
		if err := copyFile(o.activePath(m), dst); err != nil && !os.IsNotExist(err) {
			return ValidationResult{}, fmt.Errorf("CheckMerged snapshot %s: %w", s, err)
		}
	}
	if err := writeAtomic(filepath.Join(tmpdir, meta.Filename), jsonBytes); err != nil {
		return ValidationResult{}, fmt.Errorf("CheckMerged write target: %w", err)
	}

	if err := o.validator.Validate(context.Background(), tmpdir); err != nil {
		// Sing-box check failure is reported as a ValidationError so callers
		// can use res.Error() / iterate without distinguishing infra vs
		// content errors at the type level.
		ve := ValidationError{
			Slot:    slot,
			Kind:    "sing-box check",
			Message: err.Error(),
		}
		if s, idx, ok := o.attributeOutboundIndex(err.Error(), tmpdir); ok {
			ve.OutboundSlot, ve.OutboundIndex = s, &idx
		}
		res.Errors = append(res.Errors, ve)
		return res, nil
	}
	return res, nil // res.Ok() == true; может содержать advisory-предупреждения
}

// sing-box check error shapes attributable to a specific outbound.
// Initialize-phase indexes the MERGED outbounds array (config.d files
// concatenated in lexical filename order); decode-phase indexes the
// outbounds array of the named file only.
var (
	initializeOutboundIdxRe = regexp.MustCompile(`initialize outbound\[(\d+)\]:`)
	decodeOutboundIdxRe     = regexp.MustCompile(`decode config at (\S+): outbounds\[(\d+)\][.:]`)
)

// attributeOutboundIndex resolves a sing-box check error message to the
// slot whose file declares the failing outbound and its local index in
// that file's outbounds array. snapshotDir is the tmpdir that was handed
// to the validator (still present — caller's defer hasn't run). Returns
// false when the message matches no known shape or the index fits no
// snapshot file. Caller MUST hold o.mu.
func (o *Orchestrator) attributeOutboundIndex(msg, snapshotDir string) (Slot, int, bool) {
	type snapFile struct {
		filename  string
		slot      Slot
		outbounds int
	}
	var files []snapFile
	for s, m := range o.slots {
		data, err := os.ReadFile(filepath.Join(snapshotDir, m.Filename))
		if err != nil {
			continue // slot not in snapshot
		}
		files = append(files, snapFile{filename: m.Filename, slot: s, outbounds: countOutbounds(data)})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].filename < files[j].filename })

	if m := initializeOutboundIdxRe.FindStringSubmatch(msg); len(m) == 2 {
		n, err := strconv.Atoi(m[1])
		if err != nil {
			return "", 0, false
		}
		for _, f := range files {
			if n < f.outbounds {
				return f.slot, n, true
			}
			n -= f.outbounds
		}
		return "", 0, false
	}
	if m := decodeOutboundIdxRe.FindStringSubmatch(msg); len(m) == 3 {
		n, err := strconv.Atoi(m[2])
		if err != nil {
			return "", 0, false
		}
		base := filepath.Base(m[1])
		for _, f := range files {
			if f.filename == base && n < f.outbounds {
				return f.slot, n, true
			}
		}
	}
	return "", 0, false
}

// countOutbounds returns len(outbounds) of slot JSON, 0 when the bytes
// don't parse. An unparseable file cannot produce an `outbounds[N]`-shaped
// sing-box error anyway (decode fails before indexing into the array).
func countOutbounds(data []byte) int {
	var c struct {
		Outbounds []json.RawMessage `json:"outbounds"`
	}
	if err := json.Unmarshal(data, &c); err != nil {
		return 0
	}
	return len(c.Outbounds)
}

// SaveAndValidate atomically writes jsonBytes to the slot's active path
// after running CheckMerged. On success: arms a debounced reload. On
// validation failure: returns a non-empty ValidationResult and leaves the
// active file untouched. On infra failure (mkdir/write): returns wrapped
// error, active still untouched.
//
// One-shot convenience for callers that don't need iterative correction;
// see CheckMerged for the read-only variant used by the subscription
// filter loop (issue #221).
func (o *Orchestrator) SaveAndValidate(slot Slot, jsonBytes []byte) (ValidationResult, error) {
	o.mu.Lock()
	defer o.mu.Unlock()

	res, err := o.checkMergedLocked(slot, jsonBytes)
	if err != nil {
		return ValidationResult{}, err
	}
	if !res.Ok() {
		return res, nil
	}

	// Через saveLocked, а не своим writeAtomic в active/: путь обязан
	// выбираться по enabled, как у всех прочих писателей. Своя копия писала
	// в active/ безусловно — на выключенном слоте это дало бы файл и там и
	// там, что Bootstrap считает патологией (F84). Сегодняшние вызывающие
	// (10-tunnels, 16-awg3) AlwaysOn, так что поведение не меняется.
	if err := o.saveLocked(slot, jsonBytes); err != nil {
		return ValidationResult{}, err
	}
	o.scheduleReload()
	return res, nil // res.Ok() == true; может содержать advisory-предупреждения
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
