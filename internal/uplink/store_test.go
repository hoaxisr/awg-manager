package uplink

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestStoreUpgradesLegacySchemaAndPreservesTargetKind(t *testing.T) {
	c, _, _, _ := fixture(t)
	path := c.store.path
	legacy := diskPolicies{SchemaVersion: 1, Policies: c.store.List()}
	raw, _ := json.Marshal(legacy)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	p := s.List()[0]
	p.ID = "subscription-policy"
	p.Revision = 0
	p.Target.Kind = "subscription"
	if _, err = s.Put(p); err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var saved diskPolicies
	if err = json.Unmarshal(raw, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.SchemaVersion != 3 {
		t.Fatal("old binaries could misinterpret subscription targets")
	}
	loaded, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range loaded.List() {
		if p.ID == "subscription-policy" && p.Target.Kind == "subscription" {
			found = true
		}
	}
	if !found {
		t.Fatal("target kind lost")
	}
	saved.SchemaVersion = 1
	raw, _ = json.Marshal(saved)
	os.WriteFile(path, raw, 0600)
	if _, err = OpenStore(path); err == nil {
		t.Fatal("new target kind accepted under legacy schema")
	}
}

func TestStoreRejectsUnknownSchemaAndDuplicateOwnership(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policies.json")
	if err := os.WriteFile(path, []byte(`{"schemaVersion":4,"policies":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenStore(path); err == nil {
		t.Fatal("unknown schema accepted")
	}
	c, _, _, _ := fixture(t)
	p := c.List()[0].Policy
	p.ID = "duplicate"
	p.Revision = 0
	if _, err := c.store.Put(p); err == nil {
		t.Fatal("two controllers can own the same target")
	}
}

func TestFailedWriteDoesNotPublishRevision(t *testing.T) {
	c, _, _, _ := fixture(t)
	p := c.List()[0].Policy
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	c.store.path = filepath.Join(blocker, "policies.json")
	p.Enabled = false
	if _, err := c.store.Put(p); err == nil {
		t.Fatal("expected disk failure")
	}
	stored := c.store.List()[0]
	if stored.Revision != p.Revision || !stored.Enabled {
		t.Fatal("failed write published a policy", stored)
	}
}
