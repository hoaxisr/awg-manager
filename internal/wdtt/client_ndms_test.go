package wdtt

import (
	"context"
	"testing"
)

func TestEnsureClientDeviceID(t *testing.T) {
	dir := t.TempDir()
	s := NewService(dir, dir, "/bin/sh", "/bin/sh")
	cfg := DefaultClientConfig()
	cfg.ConnMode = ConnModeRaw

	got, err := s.ensureClientDeviceID(DefaultInstanceID, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got.DeviceID != "awgm-"+DefaultInstanceID {
		t.Fatalf("DeviceID=%q want awgm-%s", got.DeviceID, DefaultInstanceID)
	}
	full, err := s.GetConfig()
	if err != nil {
		t.Fatal(err)
	}
	if full.Clients[0].Config.DeviceID != "awgm-"+DefaultInstanceID {
		t.Fatalf("persisted DeviceID=%q", full.Clients[0].Config.DeviceID)
	}

	cfg.DeviceID = "phone-uuid"
	got2, err := s.ensureClientDeviceID(DefaultInstanceID, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got2.DeviceID != "phone-uuid" {
		t.Fatalf("existing DeviceID overwritten: %q", got2.DeviceID)
	}
}

func TestConfigReservedOpkgIndices(t *testing.T) {
	full := Config{
		Servers: []ServerInstance{{
			ID:     "srv1",
			Config: ServerConfig{NdmsIface: "OpkgTun17", WgIface: "opkgtun17"},
		}},
		Clients: []ClientInstance{{
			ID:     "cl1",
			Config: ClientConfig{NdmsIface: "OpkgTun18", RawIface: "opkgtun18"},
		}},
	}
	res := configReservedOpkgIndices(full, "none", "none")
	if !res[17] || !res[18] || res[19] {
		t.Fatalf("reserved = %v", res)
	}
	res2 := configReservedOpkgIndices(full, "srv1", "cl1")
	if len(res2) != 0 {
		t.Fatalf("expected empty when skipping all, got %v", res2)
	}
}

func TestAllocateWdttOpkgIndexFromSkipsReserved(t *testing.T) {
	live := map[int]bool{17: true, 18: true}
	idx, err := allocateWdttOpkgIndexFrom(live)
	if err != nil {
		t.Fatal(err)
	}
	if idx != 19 {
		t.Fatalf("idx=%d want 19", idx)
	}
}

func TestClientOpkgNDMSDescription(t *testing.T) {
	dir := t.TempDir()
	s := NewService(dir, dir, "/bin/sh", "/bin/sh")
	s.store.Save(Config{Clients: []ClientInstance{{
		ID:   "vps1",
		Name: "4vps",
	}}})
	got := s.clientOpkgNDMSDescription("vps1")
	if got != "4vps wdtt" {
		t.Fatalf("description=%q want %q", got, "4vps wdtt")
	}
}

func TestActivateClientNDMSOpkgTunUplinkOrder(t *testing.T) {
	svc, fake := newNDMSTestService(t)
	cfg := ClientConfig{NdmsIface: "OpkgTun18", RawIface: "opkgtun18"}
	conf := RawConfPayload{ClientIP: "10.70.0.5", MTU: 1300}

	if err := svc.activateClientNDMSOpkgTun(context.Background(), DefaultInstanceID, cfg, conf); err != nil {
		t.Fatalf("activate: %v", err)
	}
	upAt := fake.index("up OpkgTun18")
	secAt := fake.index("security OpkgTun18 public")
	globalAt := fake.index("ip-global OpkgTun18")
	descAt := fake.index("description OpkgTun18")
	aclAt := fake.index("acl OpkgTun18")
	if upAt < 0 || secAt < 0 || globalAt < 0 || descAt < 0 || aclAt < 0 {
		t.Fatalf("calls=%v", fake.calls)
	}
	if secAt < upAt || globalAt < secAt || descAt < globalAt || aclAt < descAt {
		t.Fatalf("want up→public→ip-global→description→acl, got %v", fake.calls)
	}
}
