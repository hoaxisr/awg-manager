package router

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
)

// registerBaseSlot registers SlotBase with a direct outbound so ApplyStaging's
// cross-slot validation passes in orchestrated tests.
func registerBaseSlot(t *testing.T, svc *ServiceImpl, dir string) {
	t.Helper()
	if err := svc.deps.Orch.Register(orchestrator.SlotMeta{Slot: orchestrator.SlotBase, Filename: "00-base.json", AlwaysOn: true}); err != nil {
		t.Fatalf("register SlotBase: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "00-base.json"),
		[]byte(`{"outbounds":[{"tag":"direct","type":"direct"}]}`), 0644); err != nil {
		t.Fatalf("write 00-base.json: %v", err)
	}
}

func statExists(t *testing.T, path string) bool {
	t.Helper()
	_, err := os.Stat(path)
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("stat %s: %v", path, err)
	}
	return err == nil
}

// TestApplyStaging_DeletedRuleSetArtifactsRemoved: a staged rule-set delete
// keeps the inline artifacts (pinned separately by
// TestDeleteRuleSet_StagedInlineKeepsSRSCompanionFiles); applying the draft
// must reap them (issue #448: files were never deleted in production).
func TestApplyStaging_DeletedRuleSetArtifactsRemoved(t *testing.T) {
	svc, dir := newOrchedTestService(t)
	svc.deps.Singbox.(*fakeSingbox).binary = "/opt/bin/sing-box"
	withFakeRuleSetCompiler(t, func(binary string, args []string) (string, string, error) {
		writeCompiledOutput(t, args, "compiled")
		return "", "", nil
	})
	registerBaseSlot(t, svc, dir)

	if err := svc.AddRuleSet(context.Background(), RuleSet{
		Tag:   "to-delete",
		Type:  "inline",
		Rules: []map[string]any{{"domain_suffix": []any{".gone.example"}}},
	}); err != nil {
		t.Fatalf("AddRuleSet: %v", err)
	}
	if res, err := svc.ApplyStaging(context.Background()); err != nil || !res.Ok() {
		t.Fatalf("ApplyStaging: err=%v res=%s", err, res.Error())
	}
	jsonPath := filepath.Join(dir, "rule-sets", "inline", "to-delete.json")
	srsPath := filepath.Join(dir, "rule-sets", "inline", "to-delete.srs")
	if !statExists(t, jsonPath) || !statExists(t, srsPath) {
		t.Fatal("artifacts must exist after apply of the added rule-set")
	}

	if err := svc.DeleteRuleSet(context.Background(), "to-delete", false); err != nil {
		t.Fatalf("DeleteRuleSet: %v", err)
	}
	// Staged delete keeps files (a discard must be able to restore).
	if !statExists(t, jsonPath) || !statExists(t, srsPath) {
		t.Fatal("staged delete must keep the artifacts until apply")
	}

	if res, err := svc.ApplyStaging(context.Background()); err != nil || !res.Ok() {
		t.Fatalf("ApplyStaging delete: err=%v res=%s", err, res.Error())
	}
	if statExists(t, jsonPath) || statExists(t, srsPath) {
		t.Fatal("applied delete must remove the orphaned inline artifacts")
	}
}

// TestApplyStaging_RenamedRuleSetOldArtifactsRemoved: renaming an inline
// rule-set and applying must reap the OLD basename's artifacts while the new
// ones stay.
func TestApplyStaging_RenamedRuleSetOldArtifactsRemoved(t *testing.T) {
	svc, dir := newOrchedTestService(t)
	svc.deps.Singbox.(*fakeSingbox).binary = "/opt/bin/sing-box"
	withFakeRuleSetCompiler(t, func(binary string, args []string) (string, string, error) {
		writeCompiledOutput(t, args, "compiled")
		return "", "", nil
	})
	registerBaseSlot(t, svc, dir)

	if err := svc.AddRuleSet(context.Background(), RuleSet{
		Tag:   "old-name",
		Type:  "inline",
		Rules: []map[string]any{{"domain_suffix": []any{".one.example"}}},
	}); err != nil {
		t.Fatalf("AddRuleSet: %v", err)
	}
	if res, err := svc.ApplyStaging(context.Background()); err != nil || !res.Ok() {
		t.Fatalf("ApplyStaging: err=%v res=%s", err, res.Error())
	}

	if err := svc.UpdateRuleSet(context.Background(), "old-name", RuleSet{
		Tag:   "new-name",
		Type:  "inline",
		Rules: []map[string]any{{"domain_suffix": []any{".one.example"}}},
	}); err != nil {
		t.Fatalf("UpdateRuleSet rename: %v", err)
	}
	// Staged rename: old artifacts still on disk (active still references them).
	if !statExists(t, filepath.Join(dir, "rule-sets", "inline", "old-name.srs")) {
		t.Fatal("staged rename must keep the old artifacts until apply")
	}
	if res, err := svc.ApplyStaging(context.Background()); err != nil || !res.Ok() {
		t.Fatalf("ApplyStaging rename: err=%v res=%s", err, res.Error())
	}
	for _, ext := range []string{".json", ".srs"} {
		if statExists(t, filepath.Join(dir, "rule-sets", "inline", "old-name"+ext)) {
			t.Fatalf("applied rename must remove old-name%s", ext)
		}
		if !statExists(t, filepath.Join(dir, "rule-sets", "inline", "new-name"+ext)) {
			t.Fatalf("applied rename must keep new-name%s", ext)
		}
	}
}

// TestGCRuleSetArtifacts_DatOrphansSweptTokenAndTmpKept covers the boot-time
// sweep of rule-sets/dat: artifacts referenced by a config's dat-srs URL are
// kept, unreferenced ones are removed, and the token file plus in-flight
// *.tmp compiles are never touched.
func TestGCRuleSetArtifacts_DatOrphansSweptTokenAndTmpKept(t *testing.T) {
	svc, dir := newOrchedTestService(t)

	datDir := filepath.Join(dir, "rule-sets", "dat")
	if err := os.MkdirAll(datDir, 0755); err != nil {
		t.Fatal(err)
	}
	seed := []string{
		"geosite-ORPHAN.json", "geosite-ORPHAN.srs", "geosite-ORPHAN.meta.json",
		"geosite-KEPT.json", "geosite-KEPT.srs", "geosite-KEPT.meta.json",
		"token", "geosite-ORPHAN-abc123.json.tmp",
	}
	for _, name := range seed {
		if err := os.WriteFile(filepath.Join(datDir, name), []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	// Active router config references only geosite-KEPT via the dat-srs URL.
	if err := os.WriteFile(filepath.Join(dir, "21-routing.json"), []byte(`{
		"route": {"rule_set": [{
			"tag": "kept",
			"type": "remote",
			"format": "binary",
			"url": "http://127.0.0.1:8080/api/singbox/router/rulesets/dat-srs?kind=geosite&tag=KEPT&token=x"
		}]}
	}`), 0644); err != nil {
		t.Fatal(err)
	}

	svc.GCRuleSetArtifacts()

	for _, name := range []string{"geosite-ORPHAN.json", "geosite-ORPHAN.srs", "geosite-ORPHAN.meta.json"} {
		if statExists(t, filepath.Join(datDir, name)) {
			t.Errorf("orphaned %s must be removed", name)
		}
	}
	for _, name := range []string{
		"geosite-KEPT.json", "geosite-KEPT.srs", "geosite-KEPT.meta.json",
		"token", "geosite-ORPHAN-abc123.json.tmp",
	} {
		if !statExists(t, filepath.Join(datDir, name)) {
			t.Errorf("%s must survive the GC", name)
		}
	}
}

// TestGCRuleSetArtifacts_KeepsPendingAndFakeIPReferences: files referenced
// only by the PENDING router draft or by the fakeip slot must survive the
// sweep — the union covers every config that can still point at them.
func TestGCRuleSetArtifacts_KeepsPendingAndFakeIPReferences(t *testing.T) {
	svc, dir := newOrchedTestService(t)
	svc.deps.Singbox.(*fakeSingbox).binary = "/opt/bin/sing-box"
	withFakeRuleSetCompiler(t, func(binary string, args []string) (string, string, error) {
		writeCompiledOutput(t, args, "compiled")
		return "", "", nil
	})

	// Pending-only inline rule-set (AddRuleSet stages a draft).
	if err := svc.AddRuleSet(context.Background(), RuleSet{
		Tag:   "draft-only",
		Type:  "inline",
		Rules: []map[string]any{{"domain_suffix": []any{".draft.example"}}},
	}); err != nil {
		t.Fatalf("AddRuleSet: %v", err)
	}
	// FakeIP active config references a materialized inline rule-set.
	if err := os.WriteFile(filepath.Join(dir, "20-fakeip.json"), []byte(`{
		"route": {"rule_set": [{
			"tag": "fakeip-set-srs",
			"type": "local",
			"format": "binary",
			"path": "`+filepath.ToSlash(filepath.Join(dir, "rule-sets", "inline", "fakeip-set.srs"))+`"
		}]}
	}`), 0644); err != nil {
		t.Fatal(err)
	}
	inlineDir := filepath.Join(dir, "rule-sets", "inline")
	for _, name := range []string{"fakeip-set.json", "fakeip-set.srs"} {
		if err := os.WriteFile(filepath.Join(inlineDir, name), []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	// And one true orphan.
	for _, name := range []string{"orphan.json", "orphan.srs"} {
		if err := os.WriteFile(filepath.Join(inlineDir, name), []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	svc.GCRuleSetArtifacts()

	for _, name := range []string{"draft-only.json", "draft-only.srs", "fakeip-set.json", "fakeip-set.srs"} {
		if !statExists(t, filepath.Join(inlineDir, name)) {
			t.Errorf("%s must survive (pending draft / fakeip reference)", name)
		}
	}
	for _, name := range []string{"orphan.json", "orphan.srs"} {
		if statExists(t, filepath.Join(inlineDir, name)) {
			t.Errorf("%s must be removed", name)
		}
	}
}
