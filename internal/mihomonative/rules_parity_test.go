package mihomonative

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/mihomo"
)

func TestRules_All36TypesAccepted(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "native.json"))
	if err != nil {
		t.Fatal(err)
	}

	specs := mihomo.AllRuleSpecs()
	if len(specs) != 36 {
		t.Fatalf("expected 36 rule specs, got %d", len(specs))
	}

	for _, spec := range specs {
		t.Run(spec.Type, func(t *testing.T) {
			payload := spec.SamplePayload
			if spec.Type == "MATCH" {
				payload = ""
			}
			rule := Rule{
				Type:     spec.Type,
				Payload:  payload,
				Outbound: "DIRECT",
				Enabled:  true,
			}
			saved, err := store.SaveRule(rule)
			if err != nil {
				t.Fatalf("SaveRule(%s) failed: %v", spec.Type, err)
			}
			if saved.Type != spec.Type {
				t.Fatalf("expected type %s, got %s", spec.Type, saved.Type)
			}
		})
	}
}

func TestRules_SubRuleRejected(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "native.json"))
	if err != nil {
		t.Fatal(err)
	}

	rule := Rule{
		Type:     "SUB-RULE",
		Payload:  "(DOMAIN,example.com)",
		Outbound: "DIRECT",
		Enabled:  true,
	}
	_, err = store.SaveRule(rule)
	if err == nil {
		t.Fatal("expected SaveRule(SUB-RULE) to fail, got nil")
	}
	if !strings.Contains(err.Error(), "SUB-RULE is not supported in AWG Manager") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestRules_RuntimeValidationAndUnsupportedMethods(t *testing.T) {
	tmpDir := t.TempDir()
	storePath := filepath.Join(tmpDir, "native.json")
	store, err := NewStore(storePath)
	if err != nil {
		t.Fatal(err)
	}

	// 1. Initially valid store with 1 rule
	validRule, err := store.SaveRule(Rule{
		Type:     "DOMAIN",
		Payload:  "example.com",
		Outbound: "DIRECT",
		Enabled:  true,
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := store.ValidateRuntimeRules(); err != nil {
		t.Fatalf("ValidateRuntimeRules should pass on clean store: %v", err)
	}

	// 2. Inject an unsupported active rule (simulating a legacy state file)
	store.mu.Lock()
	unsupportedActive := &Rule{
		ID:       "legacy-subrule-1",
		Type:     "SUB-RULE",
		Payload:  "(DOMAIN,sub.example.com)",
		Outbound: "DIRECT",
		Enabled:  true,
	}
	unsupportedDisabled := &Rule{
		ID:       "legacy-unknown-2",
		Type:     "UNKNOWN-LEGACY-TYPE",
		Payload:  "foo",
		Outbound: "DIRECT",
		Enabled:  false,
	}
	store.data.Rules = append(store.data.Rules, unsupportedActive, unsupportedDisabled)
	store.mu.Unlock()

	// 3. ValidateRuntimeRules must fail-closed because of unsupportedActive
	err = store.ValidateRuntimeRules()
	if err == nil {
		t.Fatal("expected ValidateRuntimeRules to fail on active SUB-RULE, got nil")
	}
	if !strings.Contains(err.Error(), "legacy-subrule-1") || !strings.Contains(err.Error(), "SUB-RULE") {
		t.Fatalf("unexpected ValidateRuntimeRules error: %v", err)
	}

	// 4. ComputeUnsupportedRulesSnapshot should return both unsupported rules regardless of Enabled
	unsupported, rev := store.ComputeUnsupportedRulesSnapshot()
	if len(unsupported) != 2 {
		t.Fatalf("expected 2 unsupported rules, got %d", len(unsupported))
	}
	if !strings.HasPrefix(rev, "v1:") {
		t.Fatalf("expected revision to start with v1:, got %s", rev)
	}

	// 5. Disable the active unsupported rule -> ValidateRuntimeRules should now pass
	store.mu.Lock()
	unsupportedActive.Enabled = false
	store.mu.Unlock()

	if err := store.ValidateRuntimeRules(); err != nil {
		t.Fatalf("ValidateRuntimeRules should pass when unsupported rules are disabled: %v", err)
	}

	// Recompute snapshot after modification
	unsupported, rev = store.ComputeUnsupportedRulesSnapshot()
	ids := []string{unsupported[0].ID, unsupported[1].ID}

	// Test stale revision rejection
	_, err = store.DeleteUnsupportedRules(ids, "v1:stale-revision")
	if !errors.Is(err, ErrRulesStale) {
		t.Fatalf("expected ErrRulesStale, got %v", err)
	}

	// Test selection mismatch: partial IDs
	_, err = store.DeleteUnsupportedRules([]string{ids[0]}, rev)
	if !errors.Is(err, ErrSelectionMismatch) {
		t.Fatalf("expected ErrSelectionMismatch for partial IDs, got %v", err)
	}

	// Test selection mismatch: extra unknown ID
	_, err = store.DeleteUnsupportedRules(append(ids, "unknown-id"), rev)
	if !errors.Is(err, ErrSelectionMismatch) {
		t.Fatalf("expected ErrSelectionMismatch for extra ID, got %v", err)
	}

	// Test selection mismatch: duplicate IDs
	_, err = store.DeleteUnsupportedRules([]string{ids[0], ids[0]}, rev)
	if !errors.Is(err, ErrSelectionMismatch) {
		t.Fatalf("expected ErrSelectionMismatch for duplicate IDs, got %v", err)
	}

	// Test selection mismatch: empty string in IDs
	_, err = store.DeleteUnsupportedRules([]string{ids[0], ""}, rev)
	if !errors.Is(err, ErrSelectionMismatch) {
		t.Fatalf("expected ErrSelectionMismatch for empty string in IDs, got %v", err)
	}

	// 6. DeleteUnsupportedRules failure injection and rollback test
	// Create a store whose path cannot be written to
	unwritableFile := filepath.Join(tmpDir, "blocked_dir")
	if err := os.WriteFile(unwritableFile, []byte("regular file"), 0644); err != nil {
		t.Fatal(err)
	}
	unwritableStorePath := filepath.Join(unwritableFile, "native.json")
	brokenStore := &Store{
		path: unwritableStorePath,
		data: state{
			Version: 4,
			Rules: []*Rule{
				{ID: "r1", Type: "DOMAIN", Payload: "ok.com", Outbound: "DIRECT", Enabled: true},
				{ID: "r2", Type: "SUB-RULE", Payload: "sub", Outbound: "DIRECT", Enabled: true},
			},
		},
	}

	brokenUnsupported, brokenRev := brokenStore.ComputeUnsupportedRulesSnapshot()
	if len(brokenUnsupported) != 1 {
		t.Fatalf("expected 1 unsupported rule in brokenStore, got %d", len(brokenUnsupported))
	}
	count, err := brokenStore.DeleteUnsupportedRules([]string{brokenUnsupported[0].ID}, brokenRev)
	if err == nil {
		t.Fatal("expected DeleteUnsupportedRules to fail when disk write fails, got nil")
	}
	if count != 0 {
		t.Fatalf("expected 0 deleted count on error, got %d", count)
	}
	// Verify rollback: rules count must still be 2 in memory!
	brokenStore.mu.RLock()
	inMemoryCount := len(brokenStore.data.Rules)
	brokenStore.mu.RUnlock()
	if inMemoryCount != 2 {
		t.Fatalf("expected in-memory rules to be rolled back to 2, got %d", inMemoryCount)
	}

	// 7. Successful DeleteUnsupportedRules on real store
	deletedCount, err := store.DeleteUnsupportedRules(ids, rev)
	if err != nil {
		t.Fatalf("DeleteUnsupportedRules failed: %v", err)
	}
	if deletedCount != 2 {
		t.Fatalf("expected 2 deleted rules, got %d", deletedCount)
	}

	// Verify only valid rule remains
	remaining := store.ListRules()
	if len(remaining) != 1 || remaining[0].ID != validRule.ID {
		t.Fatalf("expected 1 remaining valid rule (%s), got %#v", validRule.ID, remaining)
	}

	// Verify UnsupportedRules returns 0 now
	if len(store.UnsupportedRules()) != 0 {
		t.Fatalf("expected 0 unsupported rules after deletion, got %d", len(store.UnsupportedRules()))
	}

	// Verify persistence by reloading from disk
	reloadedStore, err := NewStore(storePath)
	if err != nil {
		t.Fatalf("failed to reload store from disk: %v", err)
	}
	if len(reloadedStore.ListRules()) != 1 || reloadedStore.ListRules()[0].ID != validRule.ID {
		t.Fatalf("persisted rules mismatch after reload: %#v", reloadedStore.ListRules())
	}
	if len(reloadedStore.UnsupportedRules()) != 0 {
		t.Fatalf("expected 0 unsupported rules in reloaded store, got %d", len(reloadedStore.UnsupportedRules()))
	}

	// 8. DeleteUnsupportedRules rejects empty selection, empty snapshot, and invalid requests
	_, emptyRev := store.ComputeUnsupportedRulesSnapshot()
	_, err = store.DeleteUnsupportedRules(nil, emptyRev)
	if !errors.Is(err, ErrSelectionMismatch) {
		t.Fatalf("expected ErrSelectionMismatch for nil IDs, got %v", err)
	}
	_, err = store.DeleteUnsupportedRules([]string{}, emptyRev)
	if !errors.Is(err, ErrSelectionMismatch) {
		t.Fatalf("expected ErrSelectionMismatch for empty slice, got %v", err)
	}
	_, err = store.DeleteUnsupportedRules([]string{"any-id"}, emptyRev)
	if !errors.Is(err, ErrSelectionMismatch) {
		t.Fatalf("expected ErrSelectionMismatch when snapshot has no unsupported rules, got %v", err)
	}
}

func TestDeleteUnsupportedRules_PerErrorPersistence(t *testing.T) {
	setupStore := func(t *testing.T) (storePath string, ids []string, rev string) {
		t.Helper()
		dir := t.TempDir()
		storePath = filepath.Join(dir, "native.json")
		s, err := NewStore(storePath)
		if err != nil {
			t.Fatalf("failed to create store: %v", err)
		}

		_, err = s.CreateRule(RuleInput{Type: "DOMAIN", Payload: "valid.example.com", Outbound: "DIRECT"})
		if err != nil {
			t.Fatalf("failed to create valid rule: %v", err)
		}

		s.mu.Lock()
		s.data.Rules = append(s.data.Rules,
			&Rule{ID: "legacy-unsupported-1", Type: "SUB-RULE", Payload: "(DOMAIN,sub.com)", Outbound: "DIRECT", Enabled: true},
			&Rule{ID: "legacy-unsupported-2", Type: "UNKNOWN-TYPE", Payload: "foo", Outbound: "DIRECT", Enabled: false},
		)
		if err := s.saveLocked(); err != nil {
			s.mu.Unlock()
			t.Fatalf("failed to persist initial test fixture: %v", err)
		}
		s.mu.Unlock()

		reloaded, err := NewStore(storePath)
		if err != nil {
			t.Fatalf("failed to reload store fixture: %v", err)
		}
		unsupported, freshRev := reloaded.ComputeUnsupportedRulesSnapshot()
		if len(unsupported) != 2 {
			t.Fatalf("expected 2 unsupported rules in fixture, got %d", len(unsupported))
		}
		return storePath, []string{unsupported[0].ID, unsupported[1].ID}, freshRev
	}

	cases := []struct {
		name        string
		buildParams func(ids []string, rev string) ([]string, string)
		expectedErr error
	}{
		{"nil IDs", func(ids []string, rev string) ([]string, string) { return nil, rev }, ErrSelectionMismatch},
		{"empty IDs", func(ids []string, rev string) ([]string, string) { return []string{}, rev }, ErrSelectionMismatch},
		{"empty string in IDs", func(ids []string, rev string) ([]string, string) { return []string{ids[0], ""}, rev }, ErrSelectionMismatch},
		{"duplicate IDs", func(ids []string, rev string) ([]string, string) { return []string{ids[0], ids[0]}, rev }, ErrSelectionMismatch},
		{"nonexistent ID", func(ids []string, rev string) ([]string, string) { return []string{ids[0], "nonexistent-id"}, rev }, ErrSelectionMismatch},
		{"stale revision", func(ids []string, rev string) ([]string, string) { return ids, "v1:stale-rev" }, ErrRulesStale},
		{"subset of IDs", func(ids []string, rev string) ([]string, string) { return []string{ids[0]}, rev }, ErrSelectionMismatch},
		{"superset of IDs", func(ids []string, rev string) ([]string, string) { return []string{ids[0], ids[1], "extra-id"}, rev }, ErrSelectionMismatch},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			storePath, realIDs, realRev := setupStore(t)
			testIDs, testRev := tc.buildParams(realIDs, realRev)

			beforeBytes, err := os.ReadFile(storePath)
			if err != nil {
				t.Fatalf("failed to read store before operation: %v", err)
			}

			// Independent pre-operation state snapshot directly from disk bytes
			var preOpState state
			if err := json.Unmarshal(beforeBytes, &preOpState); err != nil {
				t.Fatalf("failed to unmarshal pre-operation state: %v", err)
			}

			store, err := NewStore(storePath)
			if err != nil {
				t.Fatalf("failed to open store: %v", err)
			}

			_, opErr := store.DeleteUnsupportedRules(testIDs, testRev)
			if !errors.Is(opErr, tc.expectedErr) {
				t.Fatalf("expected error %v, got %v", tc.expectedErr, opErr)
			}

			// 1. Raw byte equality
			afterBytes, err := os.ReadFile(storePath)
			if err != nil {
				t.Fatalf("failed to read store after operation: %v", err)
			}
			if !bytes.Equal(beforeBytes, afterBytes) {
				t.Fatalf("store file was modified on disk despite rejected operation")
			}

			// 2. Reopened store full struct equality against independent preOpState
			reopened, err := NewStore(storePath)
			if err != nil {
				t.Fatalf("failed to reopen store: %v", err)
			}

			reopened.mu.RLock()
			actualState := reopened.data
			reopened.mu.RUnlock()

			if !reflect.DeepEqual(preOpState, actualState) {
				t.Fatalf("reopened store state differs from pre-operation snapshot: %+v vs %+v", preOpState, actualState)
			}
		})
	}
}

func TestRules_CreateUpdateTriState(t *testing.T) {
	tmpDir := t.TempDir()
	storePath := filepath.Join(tmpDir, "native.json")
	store, err := NewStore(storePath)
	if err != nil {
		t.Fatal(err)
	}

	// 1. Create with ID specified must fail with ErrIDNotAllowed
	_, err = store.CreateRule(RuleInput{
		ID:       "client-id-123",
		Type:     "DOMAIN",
		Payload:  "example.com",
		Outbound: "DIRECT",
	})
	if !errors.Is(err, ErrIDNotAllowed) {
		t.Fatalf("expected ErrIDNotAllowed when ID is provided to CreateRule, got %v", err)
	}

	// 2. Create with Enabled == nil should default to true
	r1, err := store.CreateRule(RuleInput{
		Type:     "DOMAIN",
		Payload:  "enabled-by-default.com",
		Outbound: "DIRECT",
	})
	if err != nil {
		t.Fatalf("CreateRule failed: %v", err)
	}
	if !r1.Enabled {
		t.Fatalf("expected Enabled=true by default, got false")
	}
	if r1.ID == "" {
		t.Fatalf("expected non-empty generated ID")
	}

	// 3. Create with Enabled == &false should remain false
	f := false
	r2, err := store.CreateRule(RuleInput{
		Type:     "DOMAIN",
		Payload:  "disabled.com",
		Outbound: "DIRECT",
		Enabled:  &f,
	})
	if err != nil {
		t.Fatalf("CreateRule with enabled=false failed: %v", err)
	}
	if r2.Enabled {
		t.Fatalf("expected Enabled=false, got true")
	}

	// 4. Update nonexistent rule must fail with ErrRuleNotFound
	_, err = store.UpdateRule("nonexistent-id", RuleInput{
		Type:     "DOMAIN",
		Payload:  "foo.com",
		Outbound: "DIRECT",
	})
	if !errors.Is(err, ErrRuleNotFound) {
		t.Fatalf("expected ErrRuleNotFound, got %v", err)
	}

	// 5. Update with mismatched ID must fail with ErrIDMismatch
	_, err = store.UpdateRule(r2.ID, RuleInput{
		ID:       "different-id",
		Type:     "DOMAIN",
		Payload:  "foo.com",
		Outbound: "DIRECT",
	})
	if !errors.Is(err, ErrIDMismatch) {
		t.Fatalf("expected ErrIDMismatch, got %v", err)
	}

	// 6. Update with Enabled == nil must preserve existing state (r2 was false)
	updatedR2, err := store.UpdateRule(r2.ID, RuleInput{
		Type:     "DOMAIN-SUFFIX",
		Payload:  "disabled.com",
		Outbound: "REJECT",
	})
	if err != nil {
		t.Fatalf("UpdateRule failed: %v", err)
	}
	if updatedR2.Enabled {
		t.Fatalf("expected Enabled=false to be preserved when Enabled is nil, got true")
	}
	if updatedR2.Type != "DOMAIN-SUFFIX" || updatedR2.Outbound != "REJECT" {
		t.Fatalf("expected updated fields, got %#v", updatedR2)
	}

	// 7. Update with Enabled == &true should toggle state to true
	tr := true
	toggledR2, err := store.UpdateRule(r2.ID, RuleInput{
		Type:     "DOMAIN-SUFFIX",
		Payload:  "disabled.com",
		Outbound: "REJECT",
		Enabled:  &tr,
	})
	if err != nil {
		t.Fatalf("UpdateRule failed: %v", err)
	}
	if !toggledR2.Enabled {
		t.Fatalf("expected Enabled=true after update, got false")
	}

	// 8. Update with Enabled == &false should toggle state to false
	toggledBackR2, err := store.UpdateRule(r2.ID, RuleInput{
		Type:     "DOMAIN-SUFFIX",
		Payload:  "disabled.com",
		Outbound: "REJECT",
		Enabled:  &f,
	})
	if err != nil {
		t.Fatalf("UpdateRule failed: %v", err)
	}
	if toggledBackR2.Enabled {
		t.Fatalf("expected Enabled=false after update, got true")
	}
}
