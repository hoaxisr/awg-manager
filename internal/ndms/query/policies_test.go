package query

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms"
)

const policiesPath = "/show/rc/ip/policy"

const samplePoliciesJSON = `{
	"Policy0": {
		"description": "warp",
		"standalone": true,
		"permit": [
			{"enabled": true, "interface": "Wireguard0"},
			{"enabled": false, "interface": "Wireguard1"}
		]
	},
	"Policy1": {
		"description": "direct",
		"standalone": false
	}
}`

func TestPolicyStore_List_ParsesAndCaches(t *testing.T) {
	fg := newFakeGetter()
	fg.SetJSON(policiesPath, samplePoliciesJSON)
	s := NewPolicyStore(fg, NopLogger())

	got, err := s.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len: want 2, got %d", len(got))
	}

	var policy0 ndms.Policy
	for _, p := range got {
		if p.Name == "Policy0" {
			policy0 = p
		}
	}
	if policy0.Description != "warp" {
		t.Errorf("Policy0.Description: %q", policy0.Description)
	}
	if !policy0.Standalone {
		t.Errorf("Policy0.Standalone: want true")
	}
	if len(policy0.Interfaces) != 2 {
		t.Fatalf("Policy0.Interfaces: want 2, got %d", len(policy0.Interfaces))
	}
	if policy0.Interfaces[0].Name != "Wireguard0" || policy0.Interfaces[0].Denied {
		t.Errorf("Interfaces[0]: %#v", policy0.Interfaces[0])
	}
	if !policy0.Interfaces[1].Denied {
		t.Errorf("Interfaces[1] should be Denied")
	}

	_, _ = s.List(context.Background())
	if got := fg.Calls(policiesPath); got != 1 {
		t.Errorf("calls: want 1, got %d", got)
	}
}

func TestPolicyStore_List_ServesStaleOnError(t *testing.T) {
	fg := newFakeGetter()
	fg.SetJSON(policiesPath, samplePoliciesJSON)
	s := NewPolicyStoreWithTTL(fg, NopLogger(), 20*time.Millisecond)

	_, _ = s.List(context.Background())
	time.Sleep(30 * time.Millisecond)
	fg.SetError(policiesPath, errors.New("ndms flake"))

	got, err := s.List(context.Background())
	if err != nil {
		t.Fatalf("stale-ok: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("stale len: want 2, got %d", len(got))
	}
}

func TestPolicyStore_List_EmptyArray(t *testing.T) {
	// NDMS returns `[]` instead of `{}` when no policies are configured.
	fg := newFakeGetter()
	fg.SetJSON(policiesPath, `[]`)
	s := NewPolicyStore(fg, NopLogger())

	got, err := s.List(context.Background())
	if err != nil {
		t.Fatalf("empty array: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("len: want 0, got %d", len(got))
	}
}

func TestPolicyStore_List_SkipsNoPermitEntries(t *testing.T) {
	// `"no": true` in an RCI permit block means the corresponding
	// `no ip policy ... permit ...` line — the permit was removed but RCI
	// still renders the historical entry. Those must not appear as
	// denied interfaces, otherwise the "add interface" dropdown hides
	// the real interface (it looks already-permitted).
	const withNoEntries = `{
		"HydraRoute": {
			"permit": [
				{"no": true, "enabled": false, "interface": "PPPoE0"},
				{"enabled": true, "interface": "Wireguard0"},
				{"no": true, "enabled": false, "interface": "Proxy0"}
			]
		}
	}`
	fg := newFakeGetter()
	fg.SetJSON(policiesPath, withNoEntries)
	s := NewPolicyStore(fg, NopLogger())

	got, err := s.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 || got[0].Name != "HydraRoute" {
		t.Fatalf("got: %+v", got)
	}
	if len(got[0].Interfaces) != 1 {
		t.Fatalf("Interfaces: want 1 (Wireguard0 only), got %d: %+v", len(got[0].Interfaces), got[0].Interfaces)
	}
	if got[0].Interfaces[0].Name != "Wireguard0" || got[0].Interfaces[0].Denied {
		t.Errorf("surviving entry: %+v", got[0].Interfaces[0])
	}
	if got[0].Interfaces[0].Order != 0 {
		t.Errorf("Order: want 0 (compacted after skipping), got %d", got[0].Interfaces[0].Order)
	}
}

func TestPolicyStore_InvalidateAllForcesRefetch(t *testing.T) {
	fg := newFakeGetter()
	fg.SetJSON(policiesPath, samplePoliciesJSON)
	s := NewPolicyStore(fg, NopLogger())

	_, _ = s.List(context.Background())
	s.InvalidateAll()
	_, _ = s.List(context.Background())

	if got := fg.Calls(policiesPath); got != 2 {
		t.Errorf("calls: want 2, got %d", got)
	}
}
