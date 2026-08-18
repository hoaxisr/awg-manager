package wdttserver

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/awgmproto"
	"github.com/hoaxisr/awg-manager/internal/proxyrt"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/control"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/roles"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/roles/ndmsres"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/roles/netres"
)

type fakeLink struct{ err error }

func (f *fakeLink) State(context.Context) (awgmproto.State, error) {
	return awgmproto.State{}, f.err
}

func (f *fakeLink) Snapshot() (control.Snapshot, bool) { return control.Snapshot{}, false }

type nilRunner struct{}

func (nilRunner) Start(context.Context, []string) (int, error) { return 1, nil }
func (nilRunner) Stop(context.Context, int) error              { return nil }
func (nilRunner) AlivePID() (int, bool)                        { return 0, false }

type nilGate struct{}

func (nilGate) Check(context.Context, string, string, string, []string) error { return nil }

type nilCmds struct{ ndmsres.Commands }

// memQuery — карта фактов; тест наполняет её ПОСЛЕ New, ресурсы долгоживущие
// и читают ту же карту (пересоздавать ресурсы нельзя — в них живут защёлки).
type memQuery struct{ facts map[string]ndmsres.IfaceFacts }

func (m memQuery) Iface(_ context.Context, name string) (ndmsres.IfaceFacts, bool, error) {
	f, ok := m.facts[name]
	return f, ok, nil
}
func (m memQuery) HasIPGlobal(context.Context, string) (bool, error)     { return false, nil }
func (m memQuery) HasPermitAllACL(context.Context, string) (bool, error) { return false, nil }
func (m memQuery) HasDefaultRoute(context.Context, string) (bool, error) { return false, nil }

type nilIPT struct{}

func (nilIPT) Run(context.Context, ...string) error { return errors.New("нет правила") }
func (nilIPT) Output(context.Context, ...string) (string, error) {
	return "", nil
}

type nilFW struct{}

func (nilFW) Managed(context.Context) ([]netres.PortSpec, error) { return nil, nil }
func (nilFW) Reconcile(context.Context, []netres.PortSpec) error { return nil }

type nilAccess struct{ applied []string }

func (a *nilAccess) ApplyNATModeToInterface(_ context.Context, iface, mode, prevWAN string) (string, error) {
	a.applied = append(a.applied, "nat:"+iface+":"+mode)
	return prevWAN, nil
}

func (a *nilAccess) ApplyPolicyToInterface(_ context.Context, iface, policy string) error {
	a.applied = append(a.applied, "policy:"+iface+":"+policy)
	return nil
}

func (a *nilAccess) ApplyLANSegmentsToInterface(_ context.Context, iface, addr, mask string, segments []string) error {
	a.applied = append(a.applied, "lan:"+iface)
	return nil
}

func (a *nilAccess) EnsureInterfaceFirewallPermit(_ context.Context, iface string) error {
	a.applied = append(a.applied, "permit:"+iface)
	return nil
}

type nilIngress struct{ calls int }

func (n *nilIngress) EnsureWdttServerIngressRefs(context.Context, string, string) error {
	n.calls++
	return nil
}

func srvCfg() roles.WdttServerConfig {
	return roles.WdttServerConfig{
		Listen: "0.0.0.0:56000", WgPort: 51820, Password: "main",
		WgIface: "opkgtun17", RawIface: "opkgtun19",
		NdmsIface: "OpkgTun17", RawNdmsIface: "OpkgTun19",
		RelayMode: "wg", NatMode: "full", Policy: "none", OpenFirewall: true,
	}
}

func newRole(t *testing.T) (*Role, *nilAccess, *nilIngress, memQuery) {
	t.Helper()
	acc, ing := &nilAccess{}, &nilIngress{}
	q := memQuery{facts: map[string]ndmsres.IfaceFacts{}}
	r, err := New(Deps{
		Instance: "default", Binary: "/opt/bin/wdtt-server",
		Link: &fakeLink{err: control.ErrNoSocket}, Runner: nilRunner{}, Gate: nilGate{},
		Cmds: nilCmds{}, Query: q, IPT: nilIPT{}, FW: nilFW{},
		RunHook:       func(context.Context, string, string) error { return nil },
		EnableForward: func() error { return nil },
		IfaceExists:   func(string) bool { return true },
		KernelWAN:     func(_ context.Context, n string) (string, error) { return "eth3", nil },
		PolicyMark:    func(_ context.Context, p string) (string, error) { return "0xffffd00", nil },
		Access:        acc, Ingress: ing, Now: time.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	return r, acc, ing, q
}

func ids(res []proxyrt.Resource) []proxyrt.ResourceID {
	var out []proxyrt.ResourceID
	for _, r := range res {
		out = append(out, r.ID())
	}
	return out
}

func TestServerChainOrder(t *testing.T) {
	role, _, _, _ := newRole(t)
	got := ids(role.Resources(proxyrt.IntentEnabled, srvCfg(), proxyrt.NewObservations()))
	want := []proxyrt.ResourceID{
		"ndms_interface:wg", "ndms_interface:raw", "process",
		"ndms_address:wg", "ndms_admin_state:wg",
		"ndms_address:raw", "ndms_admin_state:raw",
		"ndms_access", "nat_rules", "forward_rules", "mss_clamp",
		"netfilter_hook", "ingress_refs", "input_port",
	}
	if len(got) != len(want) {
		t.Fatalf("состав: %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("порядок: %v, ожидали %v", got, want)
		}
	}
}

func TestServerExposeAddsPolicyExit(t *testing.T) {
	role, _, _, q := newRole(t)
	cfg := srvCfg()
	cfg.ExposeToPolicies = true
	res := role.Resources(proxyrt.IntentEnabled, cfg, proxyrt.NewObservations())
	got := ids(res)
	found := false
	for i, id := range got {
		if id == "policy_exit" {
			found = true
			// После admin_state raw, до ndms_access.
			if got[i+1] != "ndms_access" {
				t.Fatalf("policy_exit не на месте: %v", got)
			}
		}
	}
	if !found {
		t.Fatalf("ExposeToPolicies обязан добавлять policy_exit: %v", got)
	}

	// Сервер — вход: кандидатуры default route у него нет ни при каком
	// наблюдении (решение владельца 2026-08-17).
	q.facts["OpkgTun17"] = ndmsres.IfaceFacts{SecurityLevel: "public"}
	for _, r := range res {
		if r.ID() != roles.RPolicyExit {
			continue
		}
		obs, err := r.Observe(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range r.Plan(obs) {
			if s.Op == "default-candidacy" {
				t.Fatalf("сервер не претендует на default route: %v", s)
			}
		}
	}
}

func TestServerNoRoutableExit(t *testing.T) {
	// Решение владельца 2026-08-17: сервер — вход, не выход.
	role, _, _, _ := newRole(t)
	for _, id := range ids(role.Resources(proxyrt.IntentEnabled, srvCfg(), proxyrt.NewObservations())) {
		if id == "routable_exit" {
			t.Fatal("у сервера не должно быть routable_exit")
		}
	}
}

func TestServerNatGroupsFollowMode(t *testing.T) {
	role, _, _, _ := newRole(t)
	cfg := srvCfg()

	groups, err := role.natGroups(cfg)(context.Background())
	if err != nil || len(groups) == 0 {
		t.Fatalf("full: группы обязаны собраться: %v %v", groups, err)
	}

	cfg.NatMode = "none"
	groups, err = role.natGroups(cfg)(context.Background())
	if err != nil || len(groups) != 0 {
		t.Fatalf("none: правил быть не должно: %v %v", groups, err)
	}

	cfg.NatMode = "internet-only"
	cfg.NatStaticWAN = ""
	// Такой конфиг отклоняет уже Validate (I5); провайдер — вторая линия на
	// случай дрейфа конфига между прогонами.
	if _, err = role.natGroups(cfg)(context.Background()); err == nil {
		t.Fatal("internet-only без выбранного WAN — ошибка, а не молчаливый full (H1, PR #697)")
	}
}

func TestServerDNSFollowsRelayMode(t *testing.T) {
	role, _, _, _ := newRole(t)
	cfg := srvCfg() // wg
	groups, _ := role.natGroups(cfg)(context.Background())
	// wg-relay: DNAT на обоих интерфейсах (raw→10.70.66.1, wg→10.66.0.1).
	guards := map[string]bool{}
	for _, g := range groups {
		guards[g.Guard] = true
	}
	if !guards["opkgtun17"] || !guards["opkgtun19"] {
		t.Fatalf("wg-relay: DNS-перехват обязан крыть оба интерфейса: %v", guards)
	}
	cfg.RelayMode = "raw"
	groups, _ = role.natGroups(cfg)(context.Background())
	for _, g := range groups {
		for _, r := range g.Rules {
			for _, tok := range r.Spec {
				if tok == "10.66.0.1:53" {
					t.Fatal("raw-relay: DNAT на WG-шлюз не нужен")
				}
			}
		}
	}
}

func TestServerDisabledDoesNotTouchNDMSAccess(t *testing.T) {
	// I-3: у выключенного сервера НЕТ мутаций NDMS-доступа — интерфейс при
	// disabled не создаётся, а команды доступа сами кандидаты в
	// create-on-reference.
	role, acc, _, _ := newRole(t)
	res := role.Resources(proxyrt.IntentDisabled, srvCfg(), proxyrt.NewObservations())
	for _, r := range res {
		if r.ID() != roles.RNdmsAccess {
			continue
		}
		obs, err := r.Observe(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if steps := r.Plan(obs); len(steps) != 0 {
			t.Fatalf("disabled: ndms_access не должен давать шагов: %v", steps)
		}
	}
	if len(acc.applied) != 0 {
		t.Fatalf("disabled: NDMS-доступ применён: %v", acc.applied)
	}
}

func TestServerDisabledLedger(t *testing.T) {
	role, _, _, _ := newRole(t)
	got := ids(role.Resources(proxyrt.IntentDisabled, srvCfg(), proxyrt.NewObservations()))
	// Та же ведомость минус netfilter/access-хвост с пустым желаемым нельзя:
	// правила снимаются ПЕРЕХОДОМ желаемого (провайдер пустых групп), поэтому
	// netfilter-ресурсы ОСТАЮТСЯ в списке; уходит только policy_exit.
	need := map[proxyrt.ResourceID]bool{
		"process": false, "ndms_address:wg": false, "ndms_admin_state:wg": false,
		"nat_rules": false, "netfilter_hook": false, "input_port": false,
	}
	for _, id := range got {
		if _, ok := need[id]; ok {
			need[id] = true
		}
	}
	for id, seen := range need {
		if !seen {
			t.Fatalf("disabled-ведомость потеряла %s: %v", id, got)
		}
	}
}

func TestServerResourcesAreLongLived(t *testing.T) {
	// I5: reconcile зовёт Resources дважды за проход и применяет по второму
	// списку. Пересозданный ресурс терял бы ведомость разности (RuleSet.doomed,
	// InputPort.prev) и защёлки — правила прежнего желаемого не сносились бы.
	role, _, _, _ := newRole(t)
	first := role.Resources(proxyrt.IntentEnabled, srvCfg(), proxyrt.NewObservations())
	second := role.Resources(proxyrt.IntentEnabled, srvCfg(), proxyrt.NewObservations())
	if len(first) != len(second) {
		t.Fatalf("состав поплыл между вызовами: %v против %v", ids(first), ids(second))
	}
	for i := range first {
		if first[i] != second[i] {
			t.Fatalf("ресурс %s пересоздан между вызовами Resources", first[i].ID())
		}
	}
}

func TestServerInputPortsOnlyWAN(t *testing.T) {
	// Паритет serverFirewallPortSpecs (server_firewall.go:9-27): порт с
	// 0.0.0.0 открывается, локальный — нет, дубли схлопываются.
	cfg := srvCfg()
	cfg.DirectListen = "127.0.0.1:9000"
	got := inputPorts(cfg)
	want := map[int]bool{56000: true, 56001: true}
	if len(got) != len(want) {
		t.Fatalf("порты: %v", got)
	}
	for _, s := range got {
		if !want[s.Port] || s.Proto != "udp" {
			t.Fatalf("лишний порт: %v (все: %v)", s, got)
		}
	}

	cfg.OpenFirewall = false
	if got := inputPorts(cfg); len(got) != 0 {
		t.Fatalf("тумблер выключен — портов нет: %v", got)
	}
}
