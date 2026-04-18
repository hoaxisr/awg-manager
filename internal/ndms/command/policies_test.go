package command

import (
	"context"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms/query"
)

func newTestPolicyCommands(_ *testing.T) (*PolicyCommands, *fakePoster, *SaveCoordinator, *spyHookNotifier) {
	poster := &fakePoster{}
	pub := &fakePublisher{}
	sc := NewSaveCoordinator(poster, pub, 500*time.Millisecond, 5*time.Second)
	q := query.NewQueries(query.Deps{Getter: query.NewFakeGetter(), Logger: query.NopLogger(), IsOS5: func() bool { return true }})
	hn := &spyHookNotifier{}
	return NewPolicyCommands(poster, sc, q, hn), poster, sc, hn
}

func TestPolicyCommands_CreatePolicy(t *testing.T) {
	cmds, poster, sc, _ := newTestPolicyCommands(t)
	_ = cmds.CreatePolicy(context.Background(), "Policy0", "warp")
	p := poster.Payloads()[0].(map[string]any)
	pol := p["ip"].(map[string]any)["policy"].(map[string]any)["Policy0"].(map[string]any)
	if pol["description"] != "warp" {
		t.Errorf("description: %v", pol["description"])
	}
	if sc.Status().State != SaveStatePending {
		t.Errorf("save state: want Pending, got %v", sc.Status().State)
	}
}

func TestPolicyCommands_DeletePolicy(t *testing.T) {
	cmds, poster, _, _ := newTestPolicyCommands(t)
	_ = cmds.DeletePolicy(context.Background(), "Policy0")
	p := poster.Payloads()[0].(map[string]any)
	pol := p["ip"].(map[string]any)["policy"].(map[string]any)["Policy0"].(map[string]any)
	if pol["no"] != true {
		t.Errorf("no: %v", pol["no"])
	}
}

func TestPolicyCommands_SetStandalone_Enable(t *testing.T) {
	cmds, poster, _, _ := newTestPolicyCommands(t)
	_ = cmds.SetStandalone(context.Background(), "Policy0", true)
	p := poster.Payloads()[0].(map[string]any)
	pol := p["ip"].(map[string]any)["policy"].(map[string]any)["Policy0"].(map[string]any)
	if pol["standalone"] != true {
		t.Errorf("standalone enable: %v", pol["standalone"])
	}
}

func TestPolicyCommands_SetStandalone_Disable(t *testing.T) {
	cmds, poster, _, _ := newTestPolicyCommands(t)
	_ = cmds.SetStandalone(context.Background(), "Policy0", false)
	p := poster.Payloads()[0].(map[string]any)
	pol := p["ip"].(map[string]any)["policy"].(map[string]any)["Policy0"].(map[string]any)
	sa := pol["standalone"].(map[string]any)
	if sa["no"] != true {
		t.Errorf("standalone disable: %v", sa)
	}
}

func TestPolicyCommands_PermitInterface(t *testing.T) {
	cmds, poster, _, _ := newTestPolicyCommands(t)
	_ = cmds.PermitInterface(context.Background(), "Policy0", "Wireguard0", 3)
	p := poster.Payloads()[0].(map[string]any)
	permit := p["ip"].(map[string]any)["policy"].(map[string]any)["Policy0"].(map[string]any)["permit"].(map[string]any)
	if permit["interface"] != "Wireguard0" || permit["order"] != 3 || permit["global"] != true {
		t.Errorf("permit: %#v", permit)
	}
}

func TestPolicyCommands_DenyInterface(t *testing.T) {
	cmds, poster, _, _ := newTestPolicyCommands(t)
	_ = cmds.DenyInterface(context.Background(), "Policy0", "Wireguard0")
	p := poster.Payloads()[0].(map[string]any)
	permit := p["ip"].(map[string]any)["policy"].(map[string]any)["Policy0"].(map[string]any)["permit"].(map[string]any)
	if permit["no"] != true || permit["interface"] != "Wireguard0" {
		t.Errorf("deny: %#v", permit)
	}
}

func TestPolicyCommands_AssignDevice(t *testing.T) {
	cmds, poster, sc, _ := newTestPolicyCommands(t)
	_ = cmds.AssignDevice(context.Background(), "aa:bb:cc:dd:ee:ff", "Policy0")
	p := poster.Payloads()[0].(map[string]any)
	host := p["ip"].(map[string]any)["hotspot"].(map[string]any)["host"].(map[string]any)
	if host["mac"] != "aa:bb:cc:dd:ee:ff" || host["policy"] != "Policy0" {
		t.Errorf("host: %#v", host)
	}
	if sc.Status().State != SaveStatePending {
		t.Errorf("save state: want Pending, got %v", sc.Status().State)
	}
}

func TestPolicyCommands_UnassignDevice(t *testing.T) {
	cmds, poster, _, _ := newTestPolicyCommands(t)
	_ = cmds.UnassignDevice(context.Background(), "aa:bb:cc:dd:ee:ff")
	p := poster.Payloads()[0].(map[string]any)
	host := p["ip"].(map[string]any)["hotspot"].(map[string]any)["host"].(map[string]any)
	pol := host["policy"].(map[string]any)
	if pol["no"] != true {
		t.Errorf("unassign: %#v", pol)
	}
}

