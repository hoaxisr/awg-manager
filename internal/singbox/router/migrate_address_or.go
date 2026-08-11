package router

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// MigrateAddressOrRules rewrites every persisted route rule that mixes a
// rule_set with the rule's own destination-address matchers into the explicit
// logical(or) form — see normalizeAddressOrRule for why such a flat rule
// matches almost nothing (issue #699). Without this pass an already-broken
// rule stays broken until the user happens to re-save it by hand.
//
// Sweeps ALL *.json across active, disabled/ and pending/ the same way
// MigrateRuleSetURLsToFork does: rules live in more than one slot. The one
// exception is the expert editor's slot — its content is authored by hand and
// owned by the user, so we do not rewrite it behind their back; the editor
// warns about such a rule instead (FlatAddressOrRuleIndexes).
//
// Rewriting goes through a generic map so fields our structs do not model
// survive; a rule that would lose a key in the typed round-trip is left
// untouched rather than silently truncated. Idempotent — a normalized rule
// carries `type`, which the predicate excludes.
//
// Returns changed=true when at least one file was rewritten, so the caller can
// reload a surviving sing-box instead of waiting for an unrelated write.
func MigrateAddressOrRules(configDir string) (bool, error) {
	changed := false
	var failures []error
	for _, pat := range []string{
		filepath.Join(configDir, "*.json"),
		filepath.Join(configDir, "disabled", "*.json"),
		filepath.Join(configDir, "pending", "*.json"),
	} {
		matches, err := filepath.Glob(pat)
		if err != nil {
			return changed, fmt.Errorf("glob %s: %w", pat, err)
		}
		for _, p := range matches {
			if filepath.Base(p) == userSlotFilename() {
				continue
			}
			fileChanged, err := migrateAddressOrRulesFile(p)
			if err != nil {
				// Один нечитаемый файл не должен оставить остальные слоты
				// (в т.ч. pending/ и disabled/) с неисправленными правилами
				// до следующего запуска — доходим до конца и рапортуем всё.
				failures = append(failures, err)
				continue
			}
			changed = changed || fileChanged
		}
	}
	return changed, errors.Join(failures...)
}

// userSlotFilename resolves the expert-editor slot's file name from the
// orchestrator registry instead of hardcoding it, so a rename cannot silently
// turn the hand-authored slot back into a migration target.
func userSlotFilename() string {
	for _, meta := range orchestrator.KnownSlots() {
		if meta.Slot == orchestrator.SlotUser {
			return meta.Filename
		}
	}
	return ""
}

// FlatAddressOrRuleIndexes returns the route.rules[] indexes of rules that mix
// a rule_set with the rule's own destination addresses — the shape sing-box
// ANDs into near-uselessness (issue #699). Used to warn about hand-authored
// configs, which we deliberately do not rewrite.
func FlatAddressOrRuleIndexes(data []byte) []int {
	var cfg struct {
		Route struct {
			Rules []Rule `json:"rules"`
		} `json:"route"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil
	}
	var out []int
	for i, r := range cfg.Route.Rules {
		if r.Type == "" && normalizeAddressOrRule(r).Type != "" {
			out = append(out, i)
		}
	}
	return out
}

func migrateAddressOrRulesFile(path string) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("read %s: %w", path, err)
	}
	next, changed, err := normalizeAddressOrRulesJSON(data)
	if err != nil {
		return false, fmt.Errorf("%s: %w", path, err)
	}
	if !changed {
		return false, nil
	}
	if err := storage.AtomicWrite(path, next); err != nil {
		return false, err
	}
	return true, nil
}

// normalizeAddressOrRulesJSON applies normalizeAddressOrRule to route.rules[]
// inside a slot file, keeping every other byte of structure intact.
func normalizeAddressOrRulesJSON(data []byte) ([]byte, bool, error) {
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, false, fmt.Errorf("parse: %w", err)
	}
	route, ok := root["route"].(map[string]any)
	if !ok {
		return nil, false, nil
	}
	rules, ok := route["rules"].([]any)
	if !ok {
		return nil, false, nil
	}
	changed := false
	for i, entry := range rules {
		obj, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		next, ok := normalizeRuleMap(obj)
		if !ok {
			continue
		}
		rules[i] = next
		changed = true
	}
	if !changed {
		return nil, false, nil
	}
	route["rules"] = rules
	root["route"] = route
	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return nil, false, fmt.Errorf("marshal: %w", err)
	}
	return out, true, nil
}

// normalizeRuleMap round-trips one rule object through the typed Rule and
// reports the normalized form. ok=false means "leave this rule alone": either
// it needs no normalization, or the typed round-trip would drop a key we do
// not model (a rule is not worth a silent data loss).
func normalizeRuleMap(obj map[string]any) (map[string]any, bool) {
	raw, err := json.Marshal(obj)
	if err != nil {
		return nil, false
	}
	var r Rule
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, false
	}
	if r.Type != "" {
		// Already logical — normalizeAddressOrRule is a no-op here and
		// rewriting anyway would make the sweep non-idempotent.
		return nil, false
	}
	normalized := normalizeAddressOrRule(r)
	if normalized.Type == "" {
		return nil, false
	}
	roundTrip, err := json.Marshal(r)
	if err != nil {
		return nil, false
	}
	var back map[string]any
	if err := json.Unmarshal(roundTrip, &back); err != nil {
		return nil, false
	}
	for k := range obj {
		if _, kept := back[k]; !kept {
			return nil, false
		}
	}
	out, err := json.Marshal(normalized)
	if err != nil {
		return nil, false
	}
	var next map[string]any
	if err := json.Unmarshal(out, &next); err != nil {
		return nil, false
	}
	return next, true
}
