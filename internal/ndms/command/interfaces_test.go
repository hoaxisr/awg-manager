package command

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms"
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

// newTestInterfaceCommands — команды уходят в fakePoster (форма payload и
// ответы роутера), список интерфейсов читается у FakeNDMS: в нём уже есть
// имена, которые тесты создают, — так подтверждение после создания находит
// запись, как на роутере после принятой команды.
func newTestInterfaceCommands(_ *testing.T) (*InterfaceCommands, *fakePoster, *SaveCoordinator, *query.Queries, *spyHookNotifier) {
	poster := &fakePoster{}
	pub := &fakePublisher{}
	sc := NewSaveCoordinator(poster, pub, 500*time.Millisecond, 5*time.Second, 0, nil)
	f := query.NewFakeNDMS(ndms.Interface{ID: "OpkgTun0"}, ndms.Interface{ID: "OpkgTun10"})
	q := query.NewQueries(query.Deps{Getter: f, Logger: query.NopLogger(), IsOS5: func() bool { return true }})
	hn := &spyHookNotifier{}
	return NewInterfaceCommands(poster, sc, q, hn), poster, sc, q, hn
}

// confirmed — доказательство существования name для тестов формы команд:
// берётся у отдельного FakeNDMS, где name есть.
func confirmed(t *testing.T, name string) query.Confirmed {
	t.Helper()
	q := query.NewQueries(query.Deps{Getter: query.NewFakeNDMS(ndms.Interface{ID: name}), Logger: query.NopLogger()})
	c, _, ok, err := q.Interfaces.Confirm(context.Background(), name)
	if err != nil || !ok {
		t.Fatalf("confirm %s: ok=%v err=%v", name, ok, err)
	}
	return c
}

// newOracleInterfaceCommands — FakeNDMS и принимает команды, и отдаёт список:
// видно созданное, снятое, фантомы и E.
func newOracleInterfaceCommands(t *testing.T, ifaces ...ndms.Interface) (*InterfaceCommands, *query.FakeNDMS, *SaveCoordinator, *query.Queries, *spyHookNotifier) {
	t.Helper()
	f := query.NewFakeNDMS(ifaces...)
	pub := &fakePublisher{}
	sc := NewSaveCoordinator(f, pub, 500*time.Millisecond, 5*time.Second, 0, nil)
	q := query.NewQueries(query.Deps{Getter: f, Logger: query.NopLogger(), IsOS5: func() bool { return true }})
	hn := &spyHookNotifier{}
	return NewInterfaceCommands(f, sc, q, hn), f, sc, q, hn
}

func TestCreateOpkgTun_ReturnsConfirmedAndFillsCache(t *testing.T) {
	cmds, f, _, q, _ := newOracleInterfaceCommands(t)
	f.ExpectCreate("OpkgTun3")
	c, err := cmds.CreateOpkgTun(context.Background(), "OpkgTun3", "t")
	if err != nil || c.Name() != "OpkgTun3" || f.Phantoms != 0 || len(f.Created) != 1 {
		t.Fatalf("c=%v err=%v phantoms=%d created=%v", c, err, f.Phantoms, f.Created)
	}
	lists := f.ListCalls()
	if rec, _ := q.Interfaces.Get(context.Background(), "OpkgTun3"); rec == nil {
		t.Fatal("Confirm after create must land the record in the cache")
	}
	if f.ListCalls() != lists || f.E != 0 {
		t.Fatalf("Get after create: lists %d→%d, E=%d", lists, f.ListCalls(), f.E)
	}
}

// NDMS принял команду, а записи в списке нет — ошибка с именем, не Confirmed.
func TestCreateOpkgTun_AbsentAfterCreate_Error(t *testing.T) {
	cmds, poster, _, _, _ := newTestInterfaceCommands(t)
	c, err := cmds.CreateOpkgTun(context.Background(), "OpkgTun7", "t")
	if err == nil || !strings.Contains(err.Error(), "OpkgTun7") || c != (query.Confirmed{}) {
		t.Fatalf("c=%v err=%v", c, err)
	}
	if len(poster.Payloads()) != 1 {
		t.Fatalf("payloads: %d", len(poster.Payloads()))
	}
}

// Список не прочитался после создания — ошибка с именем (одним), не Confirmed.
func TestCreateOpkgTun_ConfirmListError(t *testing.T) {
	cmds, f, _, _, _ := newOracleInterfaceCommands(t)
	f.ExpectCreate("OpkgTun3")
	f.FailList(errors.New("rci down"))
	c, err := cmds.CreateOpkgTun(context.Background(), "OpkgTun3", "t")
	if err == nil || !strings.Contains(err.Error(), "rci down") || strings.Count(err.Error(), "OpkgTun3") != 1 || c != (query.Confirmed{}) {
		t.Fatalf("c=%v err=%v", c, err)
	}
}

// Настоящий отказ сноса (не «интерфейса нет») — ошибка наружу, запись в кэше остаётся.
func TestDeleteOpkgTun_RealError_KeepsRecord(t *testing.T) {
	cmds, poster, _, q, _ := newTestInterfaceCommands(t)
	c, _, _, err := q.Interfaces.Confirm(context.Background(), "OpkgTun0")
	if err != nil {
		t.Fatal(err)
	}
	poster.SetError(errors.New("interface is busy"))
	if err := cmds.DeleteOpkgTun(context.Background(), c); err == nil {
		t.Fatal("ожидалась ошибка сноса")
	}
	if _, ok, _ := q.Interfaces.Lookup(context.Background(), "OpkgTun0"); !ok {
		t.Fatal("отказ сноса не должен забывать запись")
	}
}

func TestDeleteOpkgTun_ForgetsOnSuccess(t *testing.T) {
	cmds, f, _, q, _ := newOracleInterfaceCommands(t, ndms.Interface{ID: "OpkgTun3", Type: "OpkgTun"})
	c, _, _, _ := q.Interfaces.Confirm(context.Background(), "OpkgTun3")
	lists := f.ListCalls()
	if err := cmds.DeleteOpkgTun(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := q.Interfaces.Lookup(context.Background(), "OpkgTun3"); ok || f.Has("OpkgTun3") {
		t.Fatal("record must be forgotten locally and gone on the router")
	}
	if f.ListCalls() != lists || f.E != 0 || f.Phantoms != 0 {
		t.Fatalf("delete: lists %d→%d, E=%d, phantoms=%d", lists, f.ListCalls(), f.E, f.Phantoms)
	}
}

func TestInterfaceUp_ExpectHookAfterConfirmed(t *testing.T) {
	cmds, f, _, q, hn := newOracleInterfaceCommands(t, ndms.Interface{ID: "OpkgTun3"})
	c, _, _, _ := q.Interfaces.Confirm(context.Background(), "OpkgTun3")
	_ = cmds.InterfaceUp(context.Background(), c)
	if len(hn.calls) != 1 || hn.calls[0] != (hookCall{"OpkgTun3", "running"}) {
		t.Fatalf("hook: %+v", hn.calls)
	}
	if f.Phantoms != 0 || f.E != 0 {
		t.Fatalf("phantoms=%d E=%d", f.Phantoms, f.E)
	}
}

func TestInterfaceCommands_CreateOpkgTun(t *testing.T) {
	cmds, poster, sc, _, _ := newTestInterfaceCommands(t)
	if _, err := cmds.CreateOpkgTun(context.Background(), "OpkgTun0", "test"); err != nil {
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

func TestCreateOpkgTunWithSecurityLevel_Private(t *testing.T) {
	cmds, poster, _, _, _ := newTestInterfaceCommands(t)
	if _, err := cmds.CreateOpkgTunWithSecurityLevel(context.Background(), "OpkgTun10", "fakeip-tun", "private"); err != nil {
		t.Fatalf("create: %v", err)
	}
	iface := poster.Payloads()[0].(map[string]any)["interface"].(map[string]any)["OpkgTun10"].(map[string]any)
	sl := iface["security-level"].(map[string]any)
	if sl["private"] != true {
		t.Errorf("want security-level.private=true, got %#v", sl)
	}
	if _, hasPublic := sl["public"]; hasPublic {
		t.Errorf("public must not be set in private mode: %#v", sl)
	}
}

func TestInterfaceCommands_DeleteOpkgTun(t *testing.T) {
	cmds, poster, _, _, _ := newTestInterfaceCommands(t)
	if err := cmds.DeleteOpkgTun(context.Background(), confirmed(t, "OpkgTun0")); err != nil {
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
	if err := cmds.SetAddress(context.Background(), confirmed(t, "OpkgTun0"), "10.0.0.2", "255.255.255.255"); err != nil {
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
	if err := cmds.SetMTU(context.Background(), confirmed(t, "OpkgTun0"), 1280); err != nil {
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
	if err := cmds.InterfaceUp(context.Background(), confirmed(t, "OpkgTun0")); err != nil {
		t.Fatalf("InterfaceUp: %v", err)
	}
	if !reflect.DeepEqual(hn.calls, []hookCall{{"OpkgTun0", "running"}}) {
		t.Errorf("ExpectHook calls: %#v", hn.calls)
	}
}

func TestInterfaceCommands_InterfaceDown_WithHookNotifier(t *testing.T) {
	cmds, _, _, _, hn := newTestInterfaceCommands(t)
	if err := cmds.InterfaceDown(context.Background(), confirmed(t, "OpkgTun0")); err != nil {
		t.Fatalf("InterfaceDown: %v", err)
	}
	if !reflect.DeepEqual(hn.calls, []hookCall{{"OpkgTun0", "disabled"}}) {
		t.Errorf("ExpectHook calls: %#v", hn.calls)
	}
}

func TestInterfaceCommands_InterfaceUp_NilHookNotifier(t *testing.T) {
	poster := &fakePoster{}
	pub := &fakePublisher{}
	sc := NewSaveCoordinator(poster, pub, 500*time.Millisecond, 5*time.Second, 0, nil)
	q := testQueries()
	cmds := NewInterfaceCommands(poster, sc, q, nil)
	if err := cmds.InterfaceUp(context.Background(), confirmed(t, "OpkgTun0")); err != nil {
		t.Fatalf("InterfaceUp (nil notifier): %v", err)
	}
	if poster.Calls() != 1 {
		t.Errorf("calls: want 1, got %d", poster.Calls())
	}
}

func TestInterfaceCommands_SetDNS_MultipleServers(t *testing.T) {
	cmds, poster, _, _, _ := newTestInterfaceCommands(t)
	servers := []string{"1.1.1.1", "8.8.8.8"}
	if err := cmds.SetDNS(context.Background(), confirmed(t, "OpkgTun0"), servers); err != nil {
		t.Fatalf("SetDNS: %v", err)
	}
	if len(poster.Payloads()) != 2 {
		t.Fatalf("payloads: want 2 (one per server), got %d", len(poster.Payloads()))
	}
}

func TestInterfaceCommands_ClearDNS_IgnoresErrors(t *testing.T) {
	cmds, poster, _, _, _ := newTestInterfaceCommands(t)
	poster.SetError(errors.New("already absent"))
	if err := cmds.ClearDNS(context.Background(), confirmed(t, "OpkgTun0"), []string{"1.1.1.1"}); err != nil {
		t.Fatalf("ClearDNS best-effort: want no error, got %v", err)
	}
}

func TestInterfaceCommands_CreateOpkgTun_PosterError(t *testing.T) {
	cmds, poster, _, _, _ := newTestInterfaceCommands(t)
	poster.SetError(errors.New("ndms rejected"))
	_, err := cmds.CreateOpkgTun(context.Background(), "OpkgTun0", "x")
	if err == nil {
		t.Fatalf("CreateOpkgTun on poster error: want error, got nil")
	}
}
