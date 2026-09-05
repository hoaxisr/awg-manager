package wdttserver

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"
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

// Обе половины сервера получают дескриптор от менеджера — связь роли обязана
// уметь attach-tun/detach-tun.
func (f *fakeLink) AttachTun(context.Context, string, *os.File) error { return f.err }
func (f *fakeLink) DetachTun(context.Context) error                   { return f.err }

type nilRunner struct{}

func (nilRunner) Start(context.Context, []string) (int, error) { return 1, nil }
func (nilRunner) Stop(context.Context, int) error              { return nil }
func (nilRunner) AlivePID() (int, bool)                        { return 0, false }

type nilGate struct{}

func (nilGate) Check(context.Context, string, string, string, []string) error { return nil }

// countCmds — счётчик мутаций NDMS: гейт валидации требуется доказывать
// нулём вызовов к роутеру, а не отсутствием шагов в плане.
type countCmds struct{ n int }

func (c *countCmds) CreateOpkgTunWithSecurityLevel(context.Context, string, string, string) error {
	c.n++
	return nil
}
func (c *countCmds) DeleteOpkgTun(context.Context, string) error              { c.n++; return nil }
func (c *countCmds) SetDescription(context.Context, string, string) error     { c.n++; return nil }
func (c *countCmds) SetSecurityLevel(context.Context, string, string) error   { c.n++; return nil }
func (c *countCmds) SetIPGlobal(context.Context, string) error                { c.n++; return nil }
func (c *countCmds) SetAddress(context.Context, string, string, string) error { c.n++; return nil }
func (c *countCmds) ClearAddress(context.Context, string) error               { c.n++; return nil }
func (c *countCmds) SetMTU(context.Context, string, int) error                { c.n++; return nil }
func (c *countCmds) InterfaceUp(context.Context, string) error                { c.n++; return nil }
func (c *countCmds) InterfaceDown(context.Context, string) error              { c.n++; return nil }
func (c *countCmds) SetPermitAllACL(context.Context, string) error            { c.n++; return nil }
func (c *countCmds) RemovePermitAllACL(context.Context, string) error         { c.n++; return nil }
func (c *countCmds) EnsureDefaultRouteCandidacy(context.Context, string) error {
	c.n++
	return nil
}

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

func (a *nilAccess) ApplyNATModeToInterface(_ context.Context, iface, mode string, prevWANs []string) ([]string, error) {
	a.applied = append(a.applied, "nat:"+iface+":"+mode)
	return prevWANs, nil
}

func (a *nilAccess) ApplyPolicyToInterface(_ context.Context, iface, policy string) error {
	a.applied = append(a.applied, "policy:"+iface+":"+policy)
	return nil
}

func (a *nilAccess) ApplyLANSegmentsToInterface(_ context.Context, iface, addr, mask string, segments []string) error {
	a.applied = append(a.applied, "lan:"+iface)
	return nil
}

type nilIngress struct{ calls int }

func (n *nilIngress) EnsureWdttServerIngressRefs(context.Context, string, string) error {
	n.calls++
	return nil
}

func srvCfg() roles.WdttServerConfig {
	return roles.WdttServerConfig{
		Listen: "0.0.0.0:56000", WgPort: 51820, WgIface: "opkgtun17", RawIface: "opkgtun19",
		NdmsIface: "OpkgTun17", RawNdmsIface: "OpkgTun19",
		RelayMode: "wg", NatMode: "full", Policy: "none", OpenFirewall: true,
	}
}

func newRole(t *testing.T) (*Role, *nilAccess, *nilIngress, memQuery) {
	t.Helper()
	r, acc, ing, q, _ := newRoleCmds(t)
	return r, acc, ing, q
}

func newRoleCmds(t *testing.T) (*Role, *nilAccess, *nilIngress, memQuery, *countCmds) {
	t.Helper()
	acc, ing := &nilAccess{}, &nilIngress{}
	q := memQuery{facts: map[string]ndmsres.IfaceFacts{}}
	cmds := &countCmds{}
	r, err := New(Deps{
		Instance: "default", Binary: "/opt/bin/wdtt-server",
		Link: &fakeLink{err: control.ErrNoSocket}, Runner: nilRunner{}, Gate: nilGate{},
		Cmds: cmds, Query: q, IPT: nilIPT{}, FW: nilFW{},
		RunHook:       func(context.Context, string, string) error { return nil },
		EnableForward: func() error { return nil },
		IfaceExists:   func(string) bool { return true },
		KernelWAN:     func(_ context.Context, n string) (string, error) { return "eth3", nil },
		Access:        acc, Ingress: ing, Now: time.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	return r, acc, ing, q, cmds
}

func ids(res []proxyrt.Resource) []proxyrt.ResourceID {
	var out []proxyrt.ResourceID
	for _, r := range res {
		out = append(out, r.ID())
	}
	return out
}

// PF7: обе NDMS-половины сервера держат ОДИН MTU. Инвариант: сервер один, и
// абонент не должен получать разный MTU в зависимости от того, каким портом
// подключился, — выбор половины это деталь связи, а не свойство канала.
// Расхождение половин допустимо только с цифрой из замера на железе.
//
// Второй ассерт (число 1280) — сторож «не менять молча», а НЕ обоснование:
// у самого значения замера нет, оно унаследовано константой. Записано в
// docs/TRACKER.md как отдельный вопрос.
func TestServerHalvesShareMTU(t *testing.T) {
	if wgMTU != rawMTU {
		t.Fatalf("половины разошлись по MTU: wg=%d raw=%d — расхождение допустимо "+
			"только по результату замера, а не по памяти", wgMTU, rawMTU)
	}
	if wgMTU != 1280 {
		t.Fatalf("MTU половин = %d, ждали 1280 — менять только по замеру, а не по памяти", wgMTU)
	}
}

func TestServerChainOrder(t *testing.T) {
	role, _, _, _ := newRole(t)
	got := ids(role.Resources(proxyrt.IntentEnabled, srvCfg(), proxyrt.NewObservations()))
	want := []proxyrt.ResourceID{
		"ndms_interface:wg", "ndms_interface:raw", "process",
		// Дескрипторы — сразу после старта процесса: обе половины сервера
		// ждут их, чтобы подняться на интерфейсе, созданном NDMS.
		"tun_handoff:wg", "tun_handoff:raw",
		"ndms_address:wg", "ndms_admin_state:wg",
		"ndms_address:raw", "ndms_admin_state:raw",
		// netfilter_hook ДО forward_rules: снос легаси FORWARD должен идти
		// после перезаписи файла хука, иначе прогон старого хука вернёт
		// правило уже после его латча в reaped.
		"ndms_access", "nat_rules", "netfilter_hook", "forward_rules",
		"mss_clamp", "ingress_refs", "input_port", "permit_absent",
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

	// none — «без подмены адреса источника», а не «сервер закрыт»: уходит
	// ТОЛЬКО маскарад, перехват :53 остаётся. Прежде здесь стояло
	// `len(groups) != 0`, и этот ассерт закреплял ранний return, из-за
	// которого у none пропадал и перехват. Метки политики в этом наборе нет
	// намеренно: принадлежность половин политике объявляет NDMS
	// (ndms_access), см. TestSeam_NDMSAccessAppliesAllSteps. Ожидания —
	// рукописные строки, чтобы не считаться тем же кодом, что и результат.
	cfg.NatMode = "none"
	cfg.Policy = "hotspot"
	groups, err = role.natGroups(cfg)(context.Background())
	if err != nil {
		t.Fatalf("none: %v", err)
	}
	got := map[string]bool{}
	for _, g := range groups {
		for _, ru := range g.Rules {
			got[ru.Table+"/"+ru.Chain+" "+strings.Join(ru.Spec, " ")] = true
			if strings.Contains(strings.Join(ru.Spec, " "), "MASQUERADE") {
				t.Errorf("none: маскарад остался: %v", ru.Spec)
			}
		}
	}
	for _, want := range []string{
		"nat/PREROUTING -i opkgtun19 -p udp --dport 53 -j DNAT --to-destination 10.70.0.1:53",
		"nat/PREROUTING -i opkgtun17 -p udp --dport 53 -j DNAT --to-destination 10.66.0.1:53",
		"/INPUT -i opkgtun19 -p udp --dport 53 -j ACCEPT",
	} {
		if !got[want] {
			t.Errorf("none: пропало правило %q, собрано:\n%v", want, keys(got))
		}
	}
	cfg.Policy = "none"

	cfg.NatMode = "internet-only"
	cfg.NatStaticWAN = ""
	// Такой конфиг отклоняет уже Validate (I5); провайдер — вторая линия на
	// случай дрейфа конфига между прогонами.
	if _, err = role.natGroups(cfg)(context.Background()); err == nil {
		t.Fatal("internet-only без выбранного WAN — ошибка, а не молчаливый full (H1, PR #697)")
	}
}

// Половины сервера работают ОБЕ и всегда, поэтому DNS-перехват не зависит от
// режима связи: тот выбирает лишь порт по умолчанию в выдаваемой ссылке.
// Цель каждого правила — шлюз своей половины; 10.70.66.1 (адрес, который форк
// ставит на raw-TUN, только когда поднимает его сам) под менеджером не
// существует, и DNAT на него уводил запросы в никуда.
func TestServerDNSCoversBothHalvesInEveryRelayMode(t *testing.T) {
	for _, mode := range []string{"wg", "raw"} {
		role, _, _, _ := newRole(t)
		cfg := srvCfg()
		cfg.RelayMode = mode
		groups, err := role.natGroups(cfg)(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		guards := map[string]bool{}
		targets := map[string]bool{}
		for _, g := range groups {
			guards[g.Guard] = true
			for _, r := range g.Rules {
				for _, tok := range r.Spec {
					targets[tok] = true
				}
			}
		}
		if !guards["opkgtun17"] || !guards["opkgtun19"] {
			t.Fatalf("режим %q: DNS-перехват обязан крыть оба интерфейса: %v", mode, guards)
		}
		if !targets["10.66.0.1:53"] || !targets["10.70.0.1:53"] {
			t.Fatalf("режим %q: DNAT обязан метить оба шлюза, got %v", mode, targets)
		}
		if targets["10.70.66.1:53"] {
			t.Fatalf("режим %q: 10.70.66.1 под менеджером не существует", mode)
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

// RT31: состав disabled-ведомости пиннится ЦЕЛИКОМ, как у клиента
// (TestRawDisabledLedgerIsExhaustive).
//
// Прежняя редакция искала подмножество нужных id и про ЛИШНИЕ ничего не
// говорила: безусловный append tun-ресурсов в выключенном состоянии проходил
// зелёным. Заодно поправлена докстрока — она называла уходящим `policy_exit`,
// а уходят на самом деле обе половины `tun_handoff` (в этом конфиге
// `policy_exit` не объявляется вовсе: тумблер `ExposeToPolicies` выключен).
//
// Netfilter- и access-ресурсы из списка НЕ уходят: правила снимаются
// переходом желаемого (провайдер пустых групп), а не пропажей ресурса.
func TestServerDisabledLedger(t *testing.T) {
	role, _, _, _ := newRole(t)
	got := ids(role.Resources(proxyrt.IntentDisabled, srvCfg(), proxyrt.NewObservations()))
	want := []proxyrt.ResourceID{
		"ndms_interface:wg", "ndms_interface:raw", "process",
		"ndms_address:wg", "ndms_admin_state:wg",
		"ndms_address:raw", "ndms_admin_state:raw",
		"ndms_access", "nat_rules", "netfilter_hook", "forward_rules",
		"mss_clamp", "ingress_refs", "input_port", "permit_absent",
	}
	if len(got) != len(want) {
		t.Fatalf("disabled-состав: %v, ожидали %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("disabled-порядок: %v, ожидали %v", got, want)
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

// --- модель таблиц: снятие правил проверяется фактом, а не вызовом ---

var errNoRule = errors.New("iptables: no chain/target/match by that name")

type modelIPT struct {
	chains map[string][]string
	runs   int // любое обращение к iptables, включая листинг
}

func newModelIPT() *modelIPT { return &modelIPT{chains: map[string][]string{}} }

func (m *modelIPT) Run(_ context.Context, args ...string) error {
	m.runs++
	table := "filter"
	if len(args) >= 2 && args[0] == "-t" {
		table, args = args[1], args[2:]
	}
	op, chain, rest := args[0], args[1], args[2:]
	key := table + "/" + chain
	switch op {
	case "-N", "-F":
		m.chains[key] = nil
		return nil
	case "-I":
		if len(rest) > 0 && rest[0] == "1" {
			rest = rest[1:]
		}
		m.chains[key] = append([]string{strings.Join(rest, " ")}, m.chains[key]...)
		return nil
	case "-A":
		m.chains[key] = append(m.chains[key], strings.Join(rest, " "))
		return nil
	case "-C":
		for _, r := range m.chains[key] {
			if r == strings.Join(rest, " ") {
				return nil
			}
		}
		return errNoRule
	case "-D":
		want := strings.Join(rest, " ")
		for i, r := range m.chains[key] {
			if r == want {
				m.chains[key] = append(m.chains[key][:i], m.chains[key][i+1:]...)
				return nil
			}
		}
		return errNoRule
	}
	return fmt.Errorf("модель не знает операции %q", op)
}

func (m *modelIPT) Output(_ context.Context, args ...string) (string, error) {
	m.runs++
	if len(args) != 4 || args[0] != "-t" || args[2] != "-S" {
		return "", fmt.Errorf("модель не знает листинга %v", args)
	}
	var b strings.Builder
	for _, r := range m.chains[args[1]+"/"+args[3]] {
		b.WriteString("-A " + args[3] + " " + r + "\n")
	}
	return b.String(), nil
}

func (m *modelIPT) rules(table, chain string) []string { return m.chains[table+"/"+chain] }

type countFW struct{ n int }

func (f *countFW) Managed(context.Context) ([]netres.PortSpec, error) { f.n++; return nil, nil }
func (f *countFW) Reconcile(context.Context, []netres.PortSpec) error { f.n++; return nil }

// serverParts — роль с моделями вместо заглушек: хук пишется в песочницу
// теста (прод-путь /opt/etc/ndm/netfilter.d не писуем), iptables и firewall
// считают обращения.
type serverParts struct {
	role     *Role
	acc      *nilAccess
	ing      *nilIngress
	cmds     *countCmds
	ipt      *modelIPT
	fw       *countFW
	hookPath string
}

func newServerParts(t *testing.T) serverParts {
	t.Helper()
	p := serverParts{
		acc: &nilAccess{}, ing: &nilIngress{}, cmds: &countCmds{},
		ipt: newModelIPT(), fw: &countFW{},
		hookPath: t.TempDir() + "/61-awgm-wdtt-forward.sh",
	}
	r, err := New(Deps{
		Instance: "default", Binary: "/opt/bin/wdtt-server",
		Link: &fakeLink{err: control.ErrNoSocket}, Runner: nilRunner{}, Gate: nilGate{},
		Cmds: p.cmds, Query: memQuery{facts: map[string]ndmsres.IfaceFacts{}},
		IPT: p.ipt, FW: p.fw,
		RunHook:       func(context.Context, string, string) error { return nil },
		EnableForward: func() error { return nil },
		IfaceExists:   func(string) bool { return true },
		KernelWAN:     func(_ context.Context, n string) (string, error) { return "eth3", nil },
		Access:        p.acc, Ingress: p.ing, Now: time.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Файл хука — в песочницу теста; ресурс тот же, меняется только путь.
	r.hook = netres.NewHook(roles.RNetfilterHook, p.hookPath, r.deps.RunHook)
	p.role = r
	return p
}

// drive доводит один ресурс до пустого плана.
func drive(t *testing.T, res proxyrt.Resource) {
	t.Helper()
	for pass := 0; pass < 5; pass++ {
		obs, err := res.Observe(context.Background())
		if err != nil {
			t.Fatalf("%s observe: %v", res.ID(), err)
		}
		steps := res.Plan(obs)
		if len(steps) == 0 {
			return
		}
		for _, s := range steps {
			if err := res.Apply(context.Background(), s); err != nil {
				t.Fatalf("%s apply %s: %v", res.ID(), s.Op, err)
			}
		}
	}
	t.Fatalf("%s не сошёлся за 5 проходов", res.ID())
}

func byID(res []proxyrt.Resource) map[proxyrt.ResourceID]proxyrt.Resource {
	out := map[proxyrt.ResourceID]proxyrt.Resource{}
	for _, r := range res {
		out[r.ID()] = r
	}
	return out
}

func TestServerDisabledEmptiesNetfilterDesired(t *testing.T) {
	// Выключение сервера обязано СНИМАТЬ MASQUERADE и netfilter.d-хук — и
	// снимать их переходом ЖЕЛАЕМОГО в пустое, а не рукописным списком (G1).
	// Страж на желаемое, а не на присутствие ресурса в списке: потеря
	// SetDesired(nil) состав ведомости не меняет.
	//
	// FORWARD-accept тут больше нет: его нет и у ВКЛЮЧЁННОГО сервера
	// (TestServerNoForwardAcceptForRawHalf).
	p := newServerParts(t)
	cfg := srvCfg()

	res := byID(p.role.Resources(proxyrt.IntentEnabled, cfg, proxyrt.NewObservations()))
	for _, id := range []proxyrt.ResourceID{roles.RNatRules, roles.RForwardRules, roles.RNetfilterHook} {
		drive(t, res[id])
	}
	if len(p.ipt.rules("nat", "POSTROUTING")) == 0 {
		t.Fatalf("включённый сервер не поставил правила: %v", p.ipt.chains)
	}
	if _, err := os.Stat(p.hookPath); err != nil {
		t.Fatalf("включённый сервер не написал хук: %v", err)
	}

	res = byID(p.role.Resources(proxyrt.IntentDisabled, cfg, proxyrt.NewObservations()))
	for _, id := range []proxyrt.ResourceID{roles.RNatRules, roles.RForwardRules, roles.RNetfilterHook} {
		drive(t, res[id])
	}
	if got := p.ipt.rules("nat", "POSTROUTING"); len(got) != 0 {
		t.Fatalf("выключенный сервер оставил MASQUERADE: %v", got)
	}
	if _, err := os.Stat(p.hookPath); !os.IsNotExist(err) {
		t.Fatalf("выключенный сервер оставил netfilter.d-хук: %v", err)
	}
}

// Безусловного FORWARD accept у роли нет НИГДЕ: ни в ресурсе forward_rules,
// ни в netfilter.d-хуке. Правила `-I FORWARD 1 -i opkgtun19 -j ACCEPT` и `-o`
// вставали перед `_NDM_ACL_IN` и security-level, и raw-абонент видел весь
// LAN, а выбор сегментов и ACL были бессильны (решение владельца 2026-09-05).
//
// Пин держит ОБА места: ресурс проверяется по живой цепочке модели, хук — по
// телу файла. Ожидание — отсутствие, поэтому цепочка проверяется целиком
// (любое правило в filter/FORWARD — уже отказ), а хук — по подстроке ACCEPT
// с именем raw-половины в любой из двух форм.
func TestServerNoForwardAcceptForRawHalf(t *testing.T) {
	p := newServerParts(t)
	cfg := srvCfg()

	res := byID(p.role.Resources(proxyrt.IntentEnabled, cfg, proxyrt.NewObservations()))
	for _, id := range []proxyrt.ResourceID{roles.RNatRules, roles.RForwardRules, roles.RNetfilterHook} {
		drive(t, res[id])
	}

	if got := p.ipt.rules("filter", "FORWARD"); len(got) != 0 {
		t.Errorf("включённый сервер поставил FORWARD-правила: %v", got)
	}
	body, err := os.ReadFile(p.hookPath)
	if err != nil {
		t.Fatalf("хук не написан: %v", err)
	}
	for _, bad := range []string{`-i "opkgtun19" -j ACCEPT`, `-o "opkgtun19" -j ACCEPT`} {
		if strings.Contains(string(body), bad) {
			t.Errorf("хук несёт FORWARD accept %q:\n%s", bad, body)
		}
	}
}

// Правило ПРЕЖНЕЙ версии на живом роутере снимается адресно. Апгрейд не
// перезаписывает таблицы: `-i opkgtun19 -j ACCEPT` метки не несёт (усыновление
// по метке слепо), в желаемом этого запуска его не было (разность желаемых
// пуста), стартовая уборка щадит объявленные интерфейсы — значит без
// именного сноса оно живёт до перезаписи таблиц движком ndm, а всё это время
// raw-абонент видит весь LAN мимо ACL.
//
// Модель ЗАСЕЯНА обеими формами: пин на факт в таблице, а не на вызов.
func TestServerSweepsLegacyForwardAcceptOfPreviousVersion(t *testing.T) {
	const in, out = "-i opkgtun19 -j ACCEPT", "-o opkgtun19 -j ACCEPT"
	p := newServerParts(t)
	p.ipt.chains["filter/FORWARD"] = []string{in, out, "-i br0 -j ACCEPT"}

	res := byID(p.role.Resources(proxyrt.IntentEnabled, srvCfg(), proxyrt.NewObservations()))
	drive(t, res[roles.RForwardRules])

	for _, gone := range []string{in, out} {
		if slices.Contains(p.ipt.rules("filter", "FORWARD"), gone) {
			t.Errorf("остаток прежней версии %q не снят: %v", gone, p.ipt.rules("filter", "FORWARD"))
		}
	}
	// Чужое правило в той же цепочке — не наше дело.
	if !slices.Contains(p.ipt.rules("filter", "FORWARD"), "-i br0 -j ACCEPT") {
		t.Errorf("снесено чужое правило: %v", p.ipt.rules("filter", "FORWARD"))
	}
}

// Порядок ведомости: файл хука переписывается ДО сноса легаси-правила.
// Сценарий, который иначе теряется: на диске лежит хук ПРЕЖНЕЙ версии, он и
// возвращает `-I FORWARD 1 -i opkgtun19 -j ACCEPT`, а движок ndm дёргает
// netfilter.d по событиям, которые порождает тот же проход (создание
// OpkgTun, адрес, admin up — всё выше по ведомости). Снеси легаси раньше
// перезаписи файла — и прогон старого хука вернёт правило уже после того,
// как ключ попал в reaped: Doom его больше не поднимет, и остаток доживёт до
// рестарта демона.
//
// Модель: после КАЖДОГО ресурса «стреляет» ndm — прогоняется тот файл хука,
// что лежит на диске в этот момент. Ресурсы доводятся в порядке ведомости,
// поэтому пин красный ровно тогда, когда forward_rules объявлен раньше
// netfilter_hook.
func TestServerHookRewrittenBeforeLegacySweep(t *testing.T) {
	const in, out = "-i opkgtun19 -j ACCEPT", "-o opkgtun19 -j ACCEPT"
	p := newServerParts(t)
	// Хук прежней версии: в нём те самые две строки FORWARD.
	oldHook := "#!/bin/sh\ncase \"$table\" in\nfilter)\n" +
		"  run -C FORWARD " + in + " || run -I FORWARD 1 " + in + "\n" +
		"  run -C FORWARD " + out + " || run -I FORWARD 1 " + out + "\n;;\nesac\n"
	if err := os.WriteFile(p.hookPath, []byte(oldHook), 0o755); err != nil {
		t.Fatal(err)
	}
	p.ipt.chains["filter/FORWARD"] = []string{in, out}

	// ndm дёрнул netfilter.d: правила ставит тот файл, что лежит на диске.
	fire := func() {
		body, err := os.ReadFile(p.hookPath)
		if err != nil {
			t.Fatal(err)
		}
		for _, rule := range []string{in, out} {
			if strings.Contains(string(body), "-I FORWARD 1 "+rule) &&
				!slices.Contains(p.ipt.rules("filter", "FORWARD"), rule) {
				p.ipt.chains["filter/FORWARD"] = append(
					[]string{rule}, p.ipt.chains["filter/FORWARD"]...)
			}
		}
	}

	netfilter := map[proxyrt.ResourceID]bool{
		roles.RNatRules: true, roles.RForwardRules: true, roles.RNetfilterHook: true,
	}
	for _, res := range p.role.Resources(proxyrt.IntentEnabled, srvCfg(), proxyrt.NewObservations()) {
		if !netfilter[res.ID()] {
			continue
		}
		drive(t, res)
		fire()
	}

	for _, gone := range []string{in, out} {
		if slices.Contains(p.ipt.rules("filter", "FORWARD"), gone) {
			t.Errorf("хук прежней версии вернул %q после сноса: %v",
				gone, p.ipt.rules("filter", "FORWARD"))
		}
	}
}

func TestServerResourcesDeclareWithoutTouchingRouter(t *testing.T) {
	// G1: Resources — чистая декларация. Приведение живёт в Apply ресурсов;
	// ни NDMS, ни iptables, ни firewall, ни доступ с ingress-ссылками за
	// сборку ведомости не трогаются НИ ПРИ КАКОМ намерении.
	for _, intent := range []proxyrt.Intent{
		proxyrt.IntentEnabled, proxyrt.IntentDisabled, proxyrt.IntentDeleted,
	} {
		p := newServerParts(t)
		p.role.Resources(intent, srvCfg(), proxyrt.NewObservations())
		if p.cmds.n != 0 {
			t.Fatalf("%s: за сборку декларации к NDMS ушло %d команд", intent, p.cmds.n)
		}
		if p.ipt.runs != 0 {
			t.Fatalf("%s: за сборку декларации ушло %d обращений к iptables", intent, p.ipt.runs)
		}
		if p.fw.n != 0 {
			t.Fatalf("%s: за сборку декларации ушло %d обращений к firewall", intent, p.fw.n)
		}
		if len(p.acc.applied) != 0 || p.ing.calls != 0 {
			t.Fatalf("%s: за сборку декларации тронуты доступ/ingress: %v, %d",
				intent, p.acc.applied, p.ing.calls)
		}
	}
}

func TestServerInvalidConfigTouchesNoNDMS(t *testing.T) {
	// I3 ревью: обе NDMS-половины объявлены ВЫШЕ процесса, а приговор
	// Validate() был только у процесса — на заведомо нерабочем конфиге
	// роутеру уезжали два create OpkgTun прежде, чем причина доходила до
	// пользователя.
	role, acc, ing, _, cmds := newRoleCmds(t)
	cfg := srvCfg()
	cfg.NatMode = "internet-only" // без NatStaticWAN — отказ Validate
	cfg.NatStaticWAN = ""

	res, phase := proxyrt.NewReconciler(role, cfg, proxyrt.ReconcileOpts{}).
		Run(context.Background(), proxyrt.IntentEnabled)

	if cmds.n != 0 {
		t.Fatalf("на невалидном конфиге к NDMS ушло %d вызовов: %+v", cmds.n, res.States)
	}
	if len(acc.applied) != 0 || ing.calls != 0 {
		t.Fatalf("на невалидном конфиге тронуты доступ/ingress: %v, %d", acc.applied, ing.calls)
	}
	if phase != proxyrt.PhaseFailed {
		t.Fatalf("фаза %v, ожидали failed: %+v", phase, res.States)
	}
	reason := ""
	for _, s := range res.States {
		if s.Status == proxyrt.StatusFailed {
			reason = s.Error
		}
	}
	if !strings.Contains(reason, "natStaticWAN") {
		t.Fatalf("приговор обязан называть причину валидации: %q (%+v)", reason, res.States)
	}
}

// G4: производителя Access/Ingress не было ни в одной задаче волны, а
// NDMSAccess.Apply и IngressRefs.Apply дереференсят их без гарда — непроведённая
// зависимость обязана падать в конструкторе, а не паникой на первом прогоне.
func TestNewPanicsOnNilAccessOrIngress(t *testing.T) {
	base := func() Deps {
		return Deps{
			Instance: "default", Binary: "/opt/bin/wdtt-server",
			Link: &fakeLink{err: control.ErrNoSocket}, Runner: nilRunner{}, Gate: nilGate{},
			Cmds: &countCmds{}, Query: memQuery{facts: map[string]ndmsres.IfaceFacts{}},
			IPT: nilIPT{}, FW: nilFW{},
			RunHook:       func(context.Context, string, string) error { return nil },
			EnableForward: func() error { return nil },
			IfaceExists:   func(string) bool { return true },
			KernelWAN:     func(_ context.Context, n string) (string, error) { return "eth3", nil },
			Access:        &nilAccess{}, Ingress: &nilIngress{}, Now: time.Now,
		}
	}
	for name, mutate := range map[string]func(*Deps){
		"Access":  func(d *Deps) { d.Access = nil },
		"Ingress": func(d *Deps) { d.Ingress = nil },
	} {
		func() {
			defer func() {
				r := recover()
				msg, _ := r.(string)
				if !strings.Contains(msg, "G4") {
					t.Fatalf("%s: паника с текстом G4 не случилась: %v", name, r)
				}
			}()
			d := base()
			mutate(&d)
			_, _ = New(d)
		}()
	}
}

// failGate — отказ пробы бинаря: единственный способ взвести паузу повторного
// старта через публичную поверхность роли.
type failGate struct{}

func (failGate) Check(context.Context, string, string, string, []string) error {
	return errors.New("пин бинаря не обновлён")
}

// Шов «роль → ресурс процесса»: правку конфига руками менеджер проводит через
// ResetStartBackoff роли, и без этого шва она уйдёт в пустоту, а сервер
// останется лежать до пяти минут после починки.
func TestResetStartBackoffReachesProcess(t *testing.T) {
	r, err := New(Deps{
		Instance: "default", Binary: "/opt/bin/wdtt-server",
		Link: &fakeLink{err: control.ErrNoSocket}, Runner: nilRunner{}, Gate: failGate{},
		Cmds: &countCmds{}, Query: memQuery{facts: map[string]ndmsres.IfaceFacts{}},
		IPT: nilIPT{}, FW: nilFW{},
		RunHook:       func(context.Context, string, string) error { return nil },
		EnableForward: func() error { return nil },
		IfaceExists:   func(string) bool { return true },
		KernelWAN:     func(_ context.Context, n string) (string, error) { return "eth3", nil },
		Access:        &nilAccess{}, Ingress: &nilIngress{}, Now: time.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	res := r.Resources(proxyrt.IntentEnabled, srvCfg(), proxyrt.NewObservations())
	var proc proxyrt.Resource
	for _, x := range res {
		if x.ID() == roles.RProcess {
			proc = x
		}
	}
	if proc == nil {
		t.Fatalf("process не объявлен: %v", ids(res))
	}
	obs, err := proc.Observe(context.Background())
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	steps := proc.Plan(obs)
	if len(steps) != 1 || steps[0].Op != "start" {
		t.Fatalf("план %+v, ожидали start", steps)
	}
	if err := proc.Apply(context.Background(), steps[0]); err == nil {
		t.Fatal("старт обязан упасть на гейте")
	}
	if proc.RecheckAfter() <= 0 {
		t.Fatal("пауза повторного старта не взведена")
	}
	r.ResetStartBackoff()
	if got := proc.RecheckAfter(); got != 0 {
		t.Fatalf("сброс роли не дошёл до процесса, пауза %v", got)
	}
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Проводка того же решения: при none роль обязана ПОСТАВИТЬ перехват :53 и
// написать netfilter-хук — до правки хук снимался вместе с маскарадом, и
// FORWARD после перезаписи таблиц ndm восстанавливала только реконсиляция
// (шаг 15 с).
func TestServerNatModeNoneKeepsDNSAndHook(t *testing.T) {
	p := newServerParts(t)
	cfg := srvCfg()
	cfg.NatMode, cfg.Policy = "none", "hotspot"

	res := byID(p.role.Resources(proxyrt.IntentEnabled, cfg, proxyrt.NewObservations()))
	for _, id := range []proxyrt.ResourceID{roles.RNatRules, roles.RForwardRules, roles.RNetfilterHook} {
		drive(t, res[id])
	}

	for _, r := range p.ipt.rules("nat", "POSTROUTING") {
		if strings.Contains(r, "MASQUERADE") {
			t.Errorf("режим none, а маскарад поставлен: %q", r)
		}
	}
	if !hasRule(p.ipt.rules("nat", "PREROUTING"),
		"-i opkgtun17 -p udp --dport 53 -j DNAT --to-destination 10.66.0.1:53") {
		t.Errorf("перехват :53 не поставлен: %v", p.ipt.rules("nat", "PREROUTING"))
	}
	body, err := os.ReadFile(p.hookPath)
	if err != nil {
		t.Fatalf("хук не написан: %v", err)
	}
	if !strings.Contains(string(body), `-i "opkgtun17" -p udp --dport 53 -j DNAT --to-destination 10.66.0.1:53`) {
		t.Errorf("хук не несёт перехват :53:\n%s", body)
	}
	// FORWARD-accept хук больше не несёт — пин на отсутствие держит
	// TestServerNoForwardAcceptForRawHalf.
	if strings.Contains(string(body), "MASQUERADE") {
		t.Errorf("хук несёт маскарад при none:\n%s", body)
	}
}

// Проводка того же владения: постоянная область усыновления объявляется в
// сборке роли, и без неё маскарад прошлого запуска демона переживает и режим
// none, и выключенный инстанс. Пин на РОЛИ, а не только на RuleSet: тест
// netres зовёт AdoptMarked сам, поэтому выпил вызова из New остался бы
// зелёным — ровно та форма дыры, что тут ловится чаще всего.
func TestServerAdoptsStaleMasqueradeWithoutMarkedDesired(t *testing.T) {
	const stale = "-s 10.70.0.0/16 ! -o opkgtun19 -m comment --comment AWGM_WDTT -j MASQUERADE"
	for _, tc := range []struct {
		name   string
		intent proxyrt.Intent
		mode   string
	}{
		{"режим none", proxyrt.IntentEnabled, "none"},
		{"инстанс выключен", proxyrt.IntentDisabled, "full"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := newServerParts(t)
			// Ядро после рестарта демона: правило прошлой жизни на месте.
			p.ipt.chains["nat/POSTROUTING"] = []string{
				stale,
				"-s 192.168.1.0/24 -j MASQUERADE", // чужое, без метки
			}
			cfg := srvCfg()
			cfg.NatMode = tc.mode
			res := byID(p.role.Resources(tc.intent, cfg, proxyrt.NewObservations()))
			drive(t, res[roles.RNatRules])

			for _, r := range p.ipt.chains["nat/POSTROUTING"] {
				if strings.Contains(r, "AWGM_WDTT") {
					t.Fatalf("маскарад прошлого запуска пережил: %v", p.ipt.chains["nat/POSTROUTING"])
				}
			}
			if !slices.Contains(p.ipt.chains["nat/POSTROUTING"], "-s 192.168.1.0/24 -j MASQUERADE") {
				t.Fatalf("чужое правило снесено: %v", p.ipt.chains["nat/POSTROUTING"])
			}
		})
	}
}

// Отпечаток желаемого обязан покрывать имя ВТОРОЙ половины: политика теперь
// применяется к обеим, и смена одного лишь rawIface (переезд половины на
// другой OpkgTun) без этого не вызвала бы повторного применения — половина
// осталась бы вне политики молча. Ревью показало, что поле в отпечатке ничем
// не защищено.
func TestNDMSAccessFingerprintCoversRawHalf(t *testing.T) {
	acc := &nilAccess{}
	a := NewNDMSAccess(roles.RNdmsAccess, acc)
	a.SetDesired("OpkgTun17", "OpkgTun19", "full", nil, "P1",
		wgGatewayAddr, wgGatewayMask, rawGatewayAddr, rawGatewayMask, nil, true)
	drive(t, a)
	before := len(acc.applied)

	// Меняется ТОЛЬКО имя raw-половины.
	a.SetDesired("OpkgTun17", "OpkgTun21", "full", nil, "P1",
		wgGatewayAddr, wgGatewayMask, rawGatewayAddr, rawGatewayMask, nil, true)
	obs, err := a.Observe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if steps := a.Plan(obs); len(steps) == 0 {
		t.Fatal("смена имени raw-половины не породила шага: отпечаток её не видит")
	}
	drive(t, a)
	if len(acc.applied) == before {
		t.Fatal("после смены raw-половины доступ не переприменён")
	}
	if want := "policy:OpkgTun21:P1"; !slices.Contains(acc.applied, want) {
		t.Fatalf("политика не доведена до новой raw-половины: %v", acc.applied)
	}
}
