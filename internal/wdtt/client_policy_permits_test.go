package wdtt

import (
	"context"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/ndms"
)

func TestScanPoliciesPermittingOpkg(t *testing.T) {
	policies := []ndms.Policy{
		{
			Name: "Policy3",
			Interfaces: []ndms.PermittedIface{
				{Name: "ISP", Order: 0},
				{Name: "OpkgTun18", Order: 1},
			},
		},
		{
			Name: "Policy4",
			Interfaces: []ndms.PermittedIface{
				{Name: "Wireguard2", Order: 0},
			},
		},
	}
	got := scanPoliciesPermittingOpkg(policies, "OpkgTun18")
	if len(got) != 1 || got[0].Name != "Policy3" || got[0].Order != 1 {
		t.Fatalf("scan = %+v, want Policy3 order 1", got)
	}
}

func TestCaptureOpkgPolicyPermitsForConfig_PreservesWhenLiveEmpty(t *testing.T) {
	dir := t.TempDir()
	s := NewService(dir, dir, "", "")
	s.policyList = &fakePolicyLister{policies: []ndms.Policy{}}
	cfg := ClientConfig{NdmsIface: "OpkgTun18", RawIface: "opkgtun18", PolicyPermits: []OpkgPolicyPermit{{Name: "Policy4", Order: 0}}}
	full := Config{Clients: []ClientInstance{{ID: "4vps", Config: cfg}}}
	if err := s.store.Save(full); err != nil {
		t.Fatal(err)
	}
	s.captureOpkgPolicyPermitsForConfig(context.Background(), cfg)
	loaded, err := s.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Clients[0].Config.PolicyPermits) != 1 || loaded.Clients[0].Config.PolicyPermits[0].Name != "Policy4" {
		t.Fatalf("preserved = %+v", loaded.Clients[0].Config.PolicyPermits)
	}
}

func TestRestoreOpkgPolicyPermits_UsesSavedList(t *testing.T) {
	permit := &fakePolicyPermitter{}
	list := &fakePolicyLister{policies: []ndms.Policy{{
		Name: "Policy4",
		Interfaces: []ndms.PermittedIface{
			{Name: "ISP", Order: 0},
		},
	}}}
	dir := t.TempDir()
	s := NewService(dir, dir, "", "")
	s.policyPermit = permit
	s.policyList = list
	cfg := ClientConfig{
		NdmsIface:     "OpkgTun18",
		RawIface:      "opkgtun18",
		PolicyPermits: []OpkgPolicyPermit{{Name: "Policy4", Order: 1}},
	}
	s.restoreOpkgPolicyPermits(context.Background(), "4vps", cfg)
	if len(permit.calls) != 1 || permit.calls[0] != "Policy4:OpkgTun18:1" {
		t.Fatalf("restore calls = %v", permit.calls)
	}
}

func TestRecordAndRemoveOpkgPolicyPermit(t *testing.T) {
	dir := t.TempDir()
	s := NewService(dir, dir, "", "")
	cfg := ClientConfig{NdmsIface: "OpkgTun18", RawIface: "opkgtun18"}
	full := Config{Clients: []ClientInstance{{ID: "4vps", Config: cfg}}}
	if err := s.store.Save(full); err != nil {
		t.Fatal(err)
	}
	s.RecordOpkgPolicyPermit(context.Background(), "OpkgTun18", "Policy4", 2)
	loaded, _ := s.store.Load()
	if len(loaded.Clients[0].Config.PolicyPermits) != 1 {
		t.Fatalf("record = %+v", loaded.Clients[0].Config.PolicyPermits)
	}
	s.RemoveOpkgPolicyPermit(context.Background(), "OpkgTun18", "Policy4")
	loaded, _ = s.store.Load()
	if len(loaded.Clients[0].Config.PolicyPermits) != 0 {
		t.Fatalf("remove = %+v", loaded.Clients[0].Config.PolicyPermits)
	}
}
