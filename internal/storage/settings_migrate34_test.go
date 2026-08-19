package storage

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// migrateToV34: зеркальные записи fakeip/policyTun сливаются в opkgTun.
func TestMigrateToV34_SingleRecord(t *testing.T) {
	// fakeip-запись одна → union Mode=fakeip-tun c payload диапазонов.
	s := &Settings{SchemaVersion: 33,
		LegacyFakeIP: &FakeIPState{Provisioned: true, Index: 1, Inet4Range: "198.18.0.0/15", Inet6Range: "fc00::/18"}}
	(&SettingsStore{}).migrateToV34(s)
	st := s.OpkgTun
	if st == nil || st.Mode != OpkgTunModeFakeIP || !st.Provisioned || st.Index != 1 ||
		st.FakeIP == nil || st.FakeIP.Inet4Range != "198.18.0.0/15" {
		t.Fatalf("union = %+v", st)
	}
	if s.LegacyFakeIP != nil || s.LegacyPolicyTun != nil {
		t.Fatal("legacy keys must be cleared")
	}
	// policy-запись одна (hold, Provisioned=false) → union policy-tun.
	s2 := &Settings{SchemaVersion: 33,
		LegacyPolicyTun: &PolicyTunState{Index: 4, NATSegments: []PolicyTunNATSegment{{Name: "Guest", PriorMode: "dynamic"}}}}
	(&SettingsStore{}).migrateToV34(s2)
	st2 := s2.OpkgTun
	if st2 == nil || st2.Mode != OpkgTunModePolicyTun || st2.Provisioned || st2.Index != 4 ||
		st2.PolicyTun == nil || len(st2.PolicyTun.NATSegments) != 1 {
		t.Fatalf("union = %+v", st2)
	}
}

// Конфликт «обе записи»: побеждает запись активного режима; NAT-записи
// проигравшей policy-записи переезжают в payload (реап их восстановит).
func TestMigrateToV34_ConflictActiveModeWins(t *testing.T) {
	s := &Settings{SchemaVersion: 33}
	s.SingboxRouter.RoutingMode = "fakeip-tun"
	s.LegacyFakeIP = &FakeIPState{Provisioned: true, Index: 2, Inet4Range: "198.18.0.0/15"}
	s.LegacyPolicyTun = &PolicyTunState{Index: 3, NATSegments: []PolicyTunNATSegment{{Name: "Guest", PriorMode: "none"}}}
	(&SettingsStore{}).migrateToV34(s)
	st := s.OpkgTun
	if st == nil || st.Mode != OpkgTunModeFakeIP || st.Index != 2 {
		t.Fatalf("union = %+v", st)
	}
	if st.PolicyTun == nil || len(st.PolicyTun.NATSegments) != 1 {
		t.Fatalf("NAT-записи проигравшей записи потеряны: %+v", st.PolicyTun)
	}
	// Не-fakeip режим → побеждает policy-tun (NAT-свидетельства + пин индекса).
	s3 := &Settings{SchemaVersion: 33}
	s3.SingboxRouter.RoutingMode = "tproxy"
	s3.LegacyFakeIP = &FakeIPState{Provisioned: true, Index: 2}
	s3.LegacyPolicyTun = &PolicyTunState{Index: 3}
	(&SettingsStore{}).migrateToV34(s3)
	if s3.OpkgTun == nil || s3.OpkgTun.Mode != OpkgTunModePolicyTun || s3.OpkgTun.Index != 3 {
		t.Fatalf("union = %+v", s3.OpkgTun)
	}
}

// Сквозной: старый settings.json с ключами fakeip/policyTun загружается,
// мигрирует и пересохраняется без легаси-ключей.
func TestMigrateToV34_LoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	raw := `{"schemaVersion":33,"fakeip":{"provisioned":true,"index":1,"inet4Range":"198.18.0.0/15"},"policyTun":{"index":3,"natSegments":[{"name":"Guest","priorMode":"none"}]},"singboxRouter":{"routingMode":"fakeip-tun"}}`
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := NewSettingsStore(dir).Load()
	if err != nil {
		t.Fatal(err)
	}
	if s.OpkgTun == nil || s.OpkgTun.Mode != OpkgTunModeFakeIP {
		t.Fatalf("union = %+v", s.OpkgTun)
	}
	if s.OpkgTun.PolicyTun == nil || len(s.OpkgTun.PolicyTun.NATSegments) != 1 {
		t.Fatalf("NAT-записи проигравшей записи потеряны: %+v", s.OpkgTun.PolicyTun)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "settings.json"))
	if !bytes.Contains(data, []byte(`"opkgTun"`)) {
		t.Fatalf("union key not saved: %s", data)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	if _, ok := m["fakeip"]; ok {
		t.Fatal("legacy key fakeip survived save")
	}
	if _, ok := m["policyTun"]; ok {
		t.Fatal("legacy key policyTun survived save")
	}
}
