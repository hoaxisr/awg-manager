package hydraroute

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"testing"
)

type fakeNDMS struct {
	getResp    json.RawMessage
	getErr     error
	lastPath   string
	postCalled int
	lastPosts  []interface{}
}

func (f *fakeNDMS) RCIGet(_ context.Context, path string) (json.RawMessage, error) {
	f.lastPath = path
	return f.getResp, f.getErr
}

func (f *fakeNDMS) RCIPost(_ context.Context, payload interface{}) (json.RawMessage, error) {
	f.postCalled++
	f.lastPosts = append(f.lastPosts, payload)
	return nil, nil
}

func TestListPolicyNames_ParsesKeys(t *testing.T) {
	resp := json.RawMessage(`{
		"Policy0": {"description": "Mallware"},
		"HydraRoute": {"description": ""}
	}`)
	ndms := &fakeNDMS{getResp: resp}
	svc := &Service{ndms: ndms}

	got, err := svc.ListPolicyNames(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sort.Strings(got)
	want := []string{"HydraRoute", "Policy0"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if ndms.lastPath != "/show/rc/ip/policy" {
		t.Errorf("called path %q, want /show/rc/ip/policy", ndms.lastPath)
	}
}

func TestListPolicyNames_NoNDMS(t *testing.T) {
	svc := &Service{}
	got, err := svc.ListPolicyNames(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty, got %v", got)
	}
}

func TestListPolicyNames_NDMSError(t *testing.T) {
	ndms := &fakeNDMS{getErr: errors.New("boom")}
	svc := &Service{ndms: ndms}

	_, err := svc.ListPolicyNames(context.Background())
	if err == nil {
		t.Fatal("expected error propagation")
	}
}

func TestEnsurePolicyInterfaces_OrderIsZeroBased(t *testing.T) {
	// Regression: Keenetic rejects 'ip policy permit order N' when N is
	// out of range. The first permit on a fresh policy MUST be order=0;
	// previously we sent order=1 and got "invalid order: 1".
	ndms := &fakeNDMS{}
	svc := &Service{ndms: ndms}

	err := svc.EnsurePolicyInterfaces(
		context.Background(),
		"NewPolicy",
		[]string{"PPPoE0", "Wireguard0", "Wireguard1"},
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(ndms.lastPosts) != 3 {
		t.Fatalf("expected 3 RCIPost calls, got %d", len(ndms.lastPosts))
	}

	wantOrders := []int{0, 1, 2}
	wantIfaces := []string{"PPPoE0", "Wireguard0", "Wireguard1"}
	for i, payload := range ndms.lastPosts {
		permit := digPermit(t, payload, "NewPolicy")
		gotOrder, ok := permit["order"].(int)
		if !ok {
			t.Fatalf("call %d: permit.order missing/wrong type: %v", i, permit["order"])
		}
		if gotOrder != wantOrders[i] {
			t.Errorf("call %d: order = %d, want %d", i, gotOrder, wantOrders[i])
		}
		if iface, _ := permit["interface"].(string); iface != wantIfaces[i] {
			t.Errorf("call %d: interface = %q, want %q", i, iface, wantIfaces[i])
		}
	}
}

// digPermit drills into the nested RCI payload to fetch the permit object.
func digPermit(t *testing.T, payload interface{}, policyName string) map[string]interface{} {
	t.Helper()
	root, ok := payload.(map[string]interface{})
	if !ok {
		t.Fatalf("payload not a map: %T", payload)
	}
	ip, _ := root["ip"].(map[string]interface{})
	policy, _ := ip["policy"].(map[string]interface{})
	named, _ := policy[policyName].(map[string]interface{})
	permit, _ := named["permit"].(map[string]interface{})
	if permit == nil {
		t.Fatalf("permit object missing from payload: %+v", root)
	}
	return permit
}
