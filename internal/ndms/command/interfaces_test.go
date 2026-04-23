package command

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms/query"
)

type spyHookNotifier struct {
	calls []hookCall
}

type hookCall struct{ Name, Level string }

func (s *spyHookNotifier) ExpectHook(name, level string) {
	s.calls = append(s.calls, hookCall{name, level})
}

func testQueries() *query.Queries {
	return query.NewQueries(query.Deps{
		Getter: query.NewFakeGetter(),
		Logger: query.NopLogger(),
		IsOS5:  func() bool { return true },
	})
}

func newTestInterfaceCommands(_ *testing.T) (*InterfaceCommands, *fakePoster, *SaveCoordinator, *query.Queries, *spyHookNotifier) {
	poster := &fakePoster{}
	pub := &fakePublisher{}
	sc := NewSaveCoordinator(poster, pub, 500*time.Millisecond, 5*time.Second)
	q := testQueries()
	hn := &spyHookNotifier{}
	return NewInterfaceCommands(poster, sc, q, hn), poster, sc, q, hn
}

func TestInterfaceCommands_CreateOpkgTun(t *testing.T) {
	cmds, poster, sc, _, _ := newTestInterfaceCommands(t)
	if err := cmds.CreateOpkgTun(context.Background(), "OpkgTun0", "test"); err != nil {
		t.Fatalf("CreateOpkgTun: %v", err)
	}
	if len(poster.Payloads()) != 1 {
		t.Fatalf("payloads: want 1, got %d", len(poster.Payloads()))
	}
	p := poster.Payloads()[0].(map[string]any)
	iface := p["interface"].(map[string]any)["OpkgTun0"].(map[string]any)
	if iface["description"] != "test" {
		t.Errorf("description: %v", iface["description"])
	}
	sec := iface["security-level"].(map[string]any)
	if sec["public"] != true {
		t.Errorf("security-level.public: %v", sec["public"])
	}
	if sc.Status().State != SaveStatePending {
		t.Errorf("save state: want Pending, got %v", sc.Status().State)
	}
}

func TestInterfaceCommands_DeleteOpkgTun(t *testing.T) {
	cmds, poster, _, _, _ := newTestInterfaceCommands(t)
	if err := cmds.DeleteOpkgTun(context.Background(), "OpkgTun0"); err != nil {
		t.Fatalf("DeleteOpkgTun: %v", err)
	}
	p := poster.Payloads()[0].(map[string]any)
	iface := p["interface"].(map[string]any)["OpkgTun0"].(map[string]any)
	if iface["no"] != true {
		t.Errorf("no: %v", iface["no"])
	}
}

func TestInterfaceCommands_SetAddress_TwoPosts(t *testing.T) {
	cmds, poster, _, _, _ := newTestInterfaceCommands(t)
	if err := cmds.SetAddress(context.Background(), "OpkgTun0", "10.0.0.2", "255.255.255.255"); err != nil {
		t.Fatalf("SetAddress: %v", err)
	}
	if len(poster.Payloads()) != 2 {
		t.Fatalf("payloads: want 2 (clear + set), got %d", len(poster.Payloads()))
	}
	p2 := poster.Payloads()[1].(map[string]any)
	addr := p2["interface"].(map[string]any)["OpkgTun0"].(map[string]any)["ip"].(map[string]any)["address"].(map[string]any)
	if addr["address"] != "10.0.0.2" || addr["mask"] != "255.255.255.255" {
		t.Errorf("address payload: %#v", addr)
	}
}

func TestInterfaceCommands_SetMTU(t *testing.T) {
	cmds, poster, _, _, _ := newTestInterfaceCommands(t)
	if err := cmds.SetMTU(context.Background(), "OpkgTun0", 1280); err != nil {
		t.Fatalf("SetMTU: %v", err)
	}
	p := poster.Payloads()[0].(map[string]any)
	ip := p["interface"].(map[string]any)["OpkgTun0"].(map[string]any)["ip"].(map[string]any)
	if ip["mtu"] != 1280 {
		t.Errorf("mtu: %v", ip["mtu"])
	}
	tcp := ip["tcp"].(map[string]any)["adjust-mss"].(map[string]any)
	if tcp["pmtu"] != true {
		t.Errorf("pmtu: %v", tcp["pmtu"])
	}
}

func TestInterfaceCommands_InterfaceUp_WithHookNotifier(t *testing.T) {
	cmds, _, _, _, hn := newTestInterfaceCommands(t)
	if err := cmds.InterfaceUp(context.Background(), "OpkgTun0"); err != nil {
		t.Fatalf("InterfaceUp: %v", err)
	}
	if !reflect.DeepEqual(hn.calls, []hookCall{{"OpkgTun0", "running"}}) {
		t.Errorf("ExpectHook calls: %#v", hn.calls)
	}
}

func TestInterfaceCommands_InterfaceDown_WithHookNotifier(t *testing.T) {
	cmds, _, _, _, hn := newTestInterfaceCommands(t)
	if err := cmds.InterfaceDown(context.Background(), "OpkgTun0"); err != nil {
		t.Fatalf("InterfaceDown: %v", err)
	}
	if !reflect.DeepEqual(hn.calls, []hookCall{{"OpkgTun0", "disabled"}}) {
		t.Errorf("ExpectHook calls: %#v", hn.calls)
	}
}

func TestInterfaceCommands_InterfaceUp_NilHookNotifier(t *testing.T) {
	poster := &fakePoster{}
	pub := &fakePublisher{}
	sc := NewSaveCoordinator(poster, pub, 500*time.Millisecond, 5*time.Second)
	q := testQueries()
	cmds := NewInterfaceCommands(poster, sc, q, nil)
	if err := cmds.InterfaceUp(context.Background(), "OpkgTun0"); err != nil {
		t.Fatalf("InterfaceUp (nil notifier): %v", err)
	}
	if poster.Calls() != 1 {
		t.Errorf("calls: want 1, got %d", poster.Calls())
	}
}

func TestInterfaceCommands_SetDNS_MultipleServers(t *testing.T) {
	cmds, poster, _, _, _ := newTestInterfaceCommands(t)
	servers := []string{"1.1.1.1", "8.8.8.8"}
	if err := cmds.SetDNS(context.Background(), "OpkgTun0", servers); err != nil {
		t.Fatalf("SetDNS: %v", err)
	}
	if len(poster.Payloads()) != 2 {
		t.Fatalf("payloads: want 2 (one per server), got %d", len(poster.Payloads()))
	}
}

func TestInterfaceCommands_ClearDNS_IgnoresErrors(t *testing.T) {
	cmds, poster, _, _, _ := newTestInterfaceCommands(t)
	poster.SetError(errors.New("already absent"))
	if err := cmds.ClearDNS(context.Background(), "OpkgTun0", []string{"1.1.1.1"}); err != nil {
		t.Fatalf("ClearDNS best-effort: want no error, got %v", err)
	}
}

func TestInterfaceCommands_CreateOpkgTun_PosterError(t *testing.T) {
	cmds, poster, _, _, _ := newTestInterfaceCommands(t)
	poster.SetError(errors.New("ndms rejected"))
	err := cmds.CreateOpkgTun(context.Background(), "OpkgTun0", "x")
	if err == nil {
		t.Fatalf("CreateOpkgTun on poster error: want error, got nil")
	}
}
