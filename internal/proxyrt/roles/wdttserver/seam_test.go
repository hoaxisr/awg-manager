package wdttserver

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/awgmproto"
	"github.com/hoaxisr/awg-manager/internal/proxyrt"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/control"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/roles"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/roles/netres"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/roles/roletest"
)

// ── фикстура шва ────────────────────────────────────────────────

// liveLink — связь с ЖИВЫМ процессом: обе половины уже прикреплены.
//
// Прикреплённость обязательна: иначе TunHandoff пойдёт открывать настоящий
// /dev/net/tun, а тест шва не имеет права зависеть от устройства машины.
// AttachTun/DetachTun поэтому отвечают отказом — дыра в этом допущении будет
// громкой, а не молчаливой.
type liveLink struct{ now func() time.Time }

func (l *liveLink) state() awgmproto.State {
	return awgmproto.State{
		Role: "server", Instance: "default", PID: 4321,
		Tuns: []awgmproto.TunState{
			{Iface: "opkgtun17", Attached: true},
			{Iface: "opkgtun19", Attached: true},
		},
	}
}
func (l *liveLink) State(context.Context) (awgmproto.State, error) { return l.state(), nil }
func (l *liveLink) Snapshot() (control.Snapshot, bool) {
	return control.Snapshot{State: l.state(), At: l.now()}, true
}
func (l *liveLink) AttachTun(context.Context, string, *os.File) error {
	panic("attach-tun в тесте шва: снимок обязан говорить, что половины уже прикреплены")
}
func (l *liveLink) DetachTun(context.Context) error { return nil }

// memFW — firewall, помнящий доведённое. Со счётчиком вместо памяти ресурс
// портов не сходился бы никогда, и цепочка не доходила бы до целевых шагов.
type memFW struct{ want []netres.PortSpec }

func (f *memFW) Managed(context.Context) ([]netres.PortSpec, error) { return f.want, nil }
func (f *memFW) Reconcile(_ context.Context, s []netres.PortSpec) error {
	f.want = append([]netres.PortSpec(nil), s...)
	return nil
}

// recAccess — записывает АРГУМЕНТЫ, а не факт вызова: именно состав аргументов
// и есть предмет шва (прежний nilAccess запоминал только имя интерфейса).
type recAccess struct {
	nat    []string
	policy []string
	lan    []string
	// foreign — чужие привязки ACL по интерфейсу, какими их видит роутер.
	foreign map[string][]string
	// foreignErr — отказ чтения по конкретному интерфейсу.
	foreignErr map[string]error
}

func (a *recAccess) ApplyNATModeToInterface(_ context.Context, iface, mode string, prevWANs []string) ([]string, error) {
	a.nat = append(a.nat, iface+"|"+mode)
	return prevWANs, nil
}
func (a *recAccess) ApplyPolicyToInterface(_ context.Context, iface, policy string) error {
	a.policy = append(a.policy, iface+"|"+policy)
	return nil
}
func (a *recAccess) ApplyLANSegmentsToInterface(_ context.Context, iface, addr, mask string, segments []string) error {
	rec := iface + "|" + addr + "|" + mask
	for _, s := range segments {
		rec += "|" + s
	}
	a.lan = append(a.lan, rec)
	return nil
}

func (a *recAccess) ForeignAccessGroups(_ context.Context, iface string) ([]string, error) {
	if err := a.foreignErr[iface]; err != nil {
		return nil, err
	}
	return a.foreign[iface], nil
}

type recIngress struct{ calls []string }

func (i *recIngress) EnsureWdttServerIngressRefs(_ context.Context, wg, raw string) error {
	i.calls = append(i.calls, wg+"|"+raw)
	return nil
}

type seamParts struct {
	role *Role
	ndms *roletest.NDMS
	acc  *recAccess
	ing  *recIngress
	ipt  *modelIPT
	// wanAsked — имена WAN, про которые роль спрашивала kernel-устройство.
	// Без записи имени тест не отличал бы «спросили про выход из конфига» от
	// «спросили про что угодно и получили тот же eth9».
	wanAsked *[]string
	// hookPath — файл netfilter-хука роли; тесты читают его тело.
	hookPath string
}

func newSeamParts(t *testing.T) seamParts {
	t.Helper()
	now := func() time.Time { return time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC) }
	asked := &[]string{}
	p := seamParts{
		ndms: roletest.NewNDMS(), acc: &recAccess{}, ing: &recIngress{}, ipt: newModelIPT(),
		wanAsked: asked,
	}
	r, err := New(Deps{
		Instance: "default", Binary: "/opt/bin/wdtt-server",
		Link: &liveLink{now: now}, Runner: nilRunner{}, Gate: nilGate{},
		Cmds: p.ndms, Query: p.ndms,
		IPT: p.ipt, FW: &memFW{},
		RunHook:       func(context.Context, string, string) error { return nil },
		EnableForward: func() error { return nil },
		IfaceExists:   func(string) bool { return true },
		KernelWAN: func(_ context.Context, n string) (string, error) {
			*asked = append(*asked, n)
			return "eth9", nil
		},
		Access: p.acc, Ingress: p.ing, Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	p.hookPath = t.TempDir() + "/61-awgm-wdtt-forward.sh"
	r.hook = netres.NewHook(roles.RNetfilterHook, p.hookPath, r.deps.RunHook)
	p.role = r
	return p
}

func seamCfg() roles.WdttServerConfig {
	c := srvCfg()
	c.NdmsIface, c.WgIface = "OpkgTun17", "opkgtun17"
	c.RawNdmsIface, c.RawIface = "OpkgTun19", "opkgtun19"
	return c
}

// ── RT34 ────────────────────────────────────────────────────────

// MTU ДОЕЗЖАЕТ до обеих NDMS-половин, а не просто задан одинаковыми
// константами в коде роли.
//
// Существующий TestServerHalvesShareMTU сравнивает `wgMTU` с `rawMTU` — то
// есть проверяет две строки кода на равенство друг другу. Замена ОБЕИХ
// констант литералом 1300 его не роняет; именно так и выглядел исторический
// дефект PF7, ради которого тот пин писался.
//
// Ожидание здесь — ЛИТЕРАЛ 1280, а не `wgMTU`/`rawMTU`: иначе ожидание
// вычислялось бы тем же кодом, что и результат, и мутант совпал бы сам с собой.
func TestSeam_MTUReachesBothHalves(t *testing.T) {
	p := newSeamParts(t)
	roletest.Converge(t, p.role, seamCfg(), proxyrt.IntentEnabled)

	for _, name := range []string{"OpkgTun17", "OpkgTun19"} {
		facts, ok := p.ndms.Snapshot(name)
		if !ok {
			t.Fatalf("половина %s не заведена в NDMS", name)
		}
		if facts.MTU != 1280 {
			t.Errorf("%s: MTU %d, ждали 1280", name, facts.MTU)
		}
	}
}

// ── RT30 ────────────────────────────────────────────────────────

// Состав MASQUERADE доезжает до netfilter: интерфейс и подсеть — те, что у
// raw-половины, а не любые.
//
// Существующий TestServerNatGroupsFollowMode смотрит только `len != 0`, и
// MASQUERADE, построенный на `lo` с `127.0.0.0/8`, его не роняет — проверено
// мутацией. Это класс дефекта H1 из PR #697.
//
// Правила здесь — рукописные строки, а не рендер через netres.MasqGroups:
// иначе ожидание считалось бы тем же кодом, что и результат.
func TestSeam_MasqueradeCompositionByMode(t *testing.T) {
	const rule = "-s 10.70.0.0/16 ! -o opkgtun19 -m comment --comment AWGM_WDTT -j MASQUERADE"

	t.Run("full: NAT на всё, кроме своей половины", func(t *testing.T) {
		p := newSeamParts(t)
		roletest.Converge(t, p.role, seamCfg(), proxyrt.IntentEnabled)
		if !hasRule(p.ipt.rules("nat", "POSTROUTING"), rule) {
			t.Fatalf("правило full не поставлено:\n%v", p.ipt.rules("nat", "POSTROUTING"))
		}
	})

	t.Run("internet-only: NAT только в выбранный WAN", func(t *testing.T) {
		p := newSeamParts(t)
		cfg := seamCfg()
		cfg.NatMode, cfg.NatStaticWANs = "internet-only", []string{"ISP2"}
		roletest.Converge(t, p.role, cfg, proxyrt.IntentEnabled)
		const want = "-s 10.70.0.0/16 -o eth9 -m comment --comment AWGM_WDTT -j MASQUERADE"
		if !hasRule(p.ipt.rules("nat", "POSTROUTING"), want) {
			t.Fatalf("правило internet-only не поставлено:\n%v", p.ipt.rules("nat", "POSTROUTING"))
		}
		// И «куда именно» спрошено ПО ИМЕНИ ИЗ КОНФИГА, а не выдумано:
		// без этого правило с eth9 могло бы получиться при любом запросе.
		// Пинится СОСТАВ спрошенного, а не количество: движок делает несколько
		// проходов, и число обращений — не контракт.
		got := *p.wanAsked
		if len(got) == 0 {
			t.Fatal("kernel-имя WAN не спрошено вовсе")
		}
		for _, n := range got {
			if n != "ISP2" {
				t.Fatalf("спросили про %q, а в конфиге ISP2 — выход выдуман", n)
			}
		}
	})

	t.Run("none: NAT не ставится вовсе", func(t *testing.T) {
		p := newSeamParts(t)
		cfg := seamCfg()
		cfg.NatMode = "none"
		roletest.Converge(t, p.role, cfg, proxyrt.IntentEnabled)
		for _, r := range p.ipt.rules("nat", "POSTROUTING") {
			if strings.Contains(r, "MASQUERADE") {
				t.Fatalf("режим none, а MASQUERADE стоит: %q", r)
			}
		}
	})
}

// ── RV6 ─────────────────────────────────────────────────────────

// Режим NAT управляет ТОЛЬКО подменой адреса источника. При none перехват :53
// стоит на ОБЕИХ половинах и в хуке — иначе абоненты режима none уходят с DNS
// мимо роутера. Проверяется на шве роли, литералами: прямой вызов natGroups в
// role_test держит построитель, а не то, что роль его результат применяет.
func TestSeam_NatModeNoneKeepsDNSInterceptOnBothHalves(t *testing.T) {
	p := newSeamParts(t)
	cfg := seamCfg()
	cfg.NatMode = "none"
	roletest.Converge(t, p.role, cfg, proxyrt.IntentEnabled)

	for _, r := range p.ipt.rules("nat", "POSTROUTING") {
		if strings.Contains(r, "MASQUERADE") {
			t.Fatalf("none: маскарад поставлен: %v", p.ipt.rules("nat", "POSTROUTING"))
		}
	}
	wantDNAT := []string{
		"-i opkgtun17 -p udp --dport 53 -j DNAT --to-destination 10.66.0.1:53",
		"-i opkgtun17 -p tcp --dport 53 -j DNAT --to-destination 10.66.0.1:53",
		"-i opkgtun19 -p udp --dport 53 -j DNAT --to-destination 10.70.0.1:53",
		"-i opkgtun19 -p tcp --dport 53 -j DNAT --to-destination 10.70.0.1:53",
	}
	for _, want := range wantDNAT {
		if !hasRule(p.ipt.rules("nat", "PREROUTING"), want) {
			t.Errorf("нет DNAT %q:\n%v", want, p.ipt.rules("nat", "PREROUTING"))
		}
	}
	body, err := os.ReadFile(p.hookPath)
	if err != nil {
		t.Fatal(err)
	}
	// В теле хука имя интерфейса после -i уходит в кавычки (hookQuoteIfaces,
	// rule.go) — форма строки хука, а не модели iptables, поэтому литералы
	// свои: подстрока снята с фактического тела файла, не вычислена.
	wantHook := []string{
		`-i "opkgtun17" -p udp --dport 53 -j DNAT --to-destination 10.66.0.1:53`,
		`-i "opkgtun17" -p tcp --dport 53 -j DNAT --to-destination 10.66.0.1:53`,
		`-i "opkgtun19" -p udp --dport 53 -j DNAT --to-destination 10.70.0.1:53`,
		`-i "opkgtun19" -p tcp --dport 53 -j DNAT --to-destination 10.70.0.1:53`,
	}
	for _, want := range wantHook {
		if !strings.Contains(string(body), want) {
			t.Errorf("в хуке нет строки %q:\n%s", want, body)
		}
	}
}

func hasRule(rules []string, want string) bool {
	for _, r := range rules {
		if r == want {
			return true
		}
	}
	return false
}

// ── RT32, RT33 ──────────────────────────────────────────────────

// Позитивный путь NDMSAccess.Apply: доводятся ВСЕ четыре шага и с теми
// аргументами, что заданы конфигом. Раньше выпотрошенное тело Apply тест не
// ронял — покрыт был только отказной путь.
//
// Адрес и маска — литералы, не константы wgGatewayAddr/wgGatewayMask.
func TestSeam_NDMSAccessAppliesAllSteps(t *testing.T) {
	p := newSeamParts(t)
	cfg := seamCfg()
	cfg.Policy = "P1"
	cfg.LanSegments = []string{"br0", "br1"}
	roletest.Converge(t, p.role, cfg, proxyrt.IntentEnabled)

	if want := "OpkgTun17|full"; !hasRule(p.acc.nat, want) {
		t.Errorf("NAT-режим не доведён: %v, ждали %q", p.acc.nat, want)
	}
	if want := "OpkgTun17|P1"; !hasRule(p.acc.policy, want) {
		t.Errorf("политика не доведена: %v, ждали %q", p.acc.policy, want)
	}
	if want := "OpkgTun17|10.66.0.1|255.255.0.0|br0|br1"; !hasRule(p.acc.lan, want) {
		t.Errorf("сегменты LAN не доведены: %v, ждали %q", p.acc.lan, want)
	}
	// LAN-ACL — на ОБЕ половины, и у raw-половины СВОЯ peer-сеть: список
	// строится от адреса интерфейса, и адрес WG-половины дал бы правила для
	// чужой сети — то есть молча открытый (или закрытый) LAN у raw-абонента.
	// Литералы, а не rawGatewayAddr/rawGatewayMask: ожидание не должно
	// вычисляться тем же кодом, что и результат.
	if want := "OpkgTun19|10.70.0.1|255.255.0.0|br0|br1"; !hasRule(p.acc.lan, want) {
		t.Errorf("сегменты LAN не доведены до raw-половины: %v, ждали %q", p.acc.lan, want)
	}
	// Политика — на ОБЕ половины: один сервер, одна принадлежность. Абонент
	// не должен маршрутизироваться по-разному в зависимости от того, каким
	// портом подключился, а половины — это выбор порта, не свойство канала.
	if want := "OpkgTun19|P1"; !hasRule(p.acc.policy, want) {
		t.Errorf("политика не доведена до raw-половины: %v, ждали %q", p.acc.policy, want)
	}
	// Разрешающего ACL на половинах быть НЕ должно: привязанный список — это
	// permit-исключения, срабатывающие ДО security-level (стенд 2026-09-02,
	// `_NDM_ACL_IN` → цепочка с одними ACCEPT, без deny-хвоста), поэтому
	// permit-all обнулял бы выбор сегментов, доведённый строкой выше.
	//
	// ЧЕСТНО ПРО СИЛУ ЭТОГО АССЕРТА (уточнено ревью). Для WG-половины он
	// красится: мутация «объявить выход без гейта ExposeToPolicies» (плюс
	// security-level public) ставит permit-all на OpkgTun17 — правда, тогда
	// краснеет и TestSeam_ServerPolicyExitFlags, то есть покрытие двойное.
	// Для RAW-половины покрасить нечем: пути поставить ей permit-all в коде не
	// осталось вовсе — держит компилятор. Там это растяжка на будущее.
	for _, iface := range []string{"OpkgTun17", "OpkgTun19"} {
		if p.ndms.ExitOf(iface).PermitAll {
			t.Errorf("на %s поставлено permit-all: выбор LAN-сегментов обнулён", iface)
		}
	}
}

// Ссылки ingress доводятся ОБЕИМИ половинами: sing-box должен знать оба
// kernel-имени, иначе трафик абонентов не попадёт в маршрутизацию.
func TestSeam_IngressRefsCarryBothHalves(t *testing.T) {
	p := newSeamParts(t)
	roletest.Converge(t, p.role, seamCfg(), proxyrt.IntentEnabled)

	if want := "opkgtun17|opkgtun19"; !hasRule(p.ing.calls, want) {
		t.Fatalf("ссылки ingress: %v, ждали пару %q", p.ing.calls, want)
	}
}

// ── RT36 (серверная половина) ───────────────────────────────────

// Сервер объявляется выходом БЕЗ кандидатуры в default route: он вход, а не
// аплинк. IPGlobal и permit-all при этом обязаны быть взведены.
//
// Второй кейс — про тумблер: без `ExposeToPolicies` ресурс выхода не
// объявляется вовсе, и признаки не трогаются. Без этой половины пин не
// отличал бы «тумблер выключен» от «шов сломан».
func TestSeam_ServerPolicyExitFlags(t *testing.T) {
	t.Run("тумблер включён: выход виден политикам, но не кандидат", func(t *testing.T) {
		p := newSeamParts(t)
		cfg := seamCfg()
		cfg.ExposeToPolicies = true
		roletest.Converge(t, p.role, cfg, proxyrt.IntentEnabled)

		got := p.ndms.ExitOf("OpkgTun17")
		if !got.IPGlobal {
			t.Error("ip global не взведён — выход не виден политикам")
		}
		if !got.PermitAll {
			t.Error("permit-all не взведён")
		}
		if got.DefaultRoute {
			t.Error("сервер объявлен кандидатом в default route: он вход, а не аплинк")
		}
	})

	t.Run("тумблер выключен: признаков выхода не появляется", func(t *testing.T) {
		p := newSeamParts(t)
		roletest.Converge(t, p.role, seamCfg(), proxyrt.IntentEnabled) // ExposeToPolicies=false
		if got := p.ndms.ExitOf("OpkgTun17"); got.IPGlobal || got.PermitAll || got.DefaultRoute {
			t.Fatalf("выход объявлен без тумблера: %+v", got)
		}
	})
}

// ── RV2: миграция permit-all ────────────────────────────────────

// Остаток прошлых версий: permit-all `_WEBADMIN_<iface>` стоит на обеих
// половинах ещё до запуска роли. Роль обязана его снять — иначе выбор
// LAN-сегментов декоративен (стенд 2026-09-02).
func TestSeam_PermitAllResidueRemovedFromBothHalves(t *testing.T) {
	p := newSeamParts(t)
	ctx := context.Background()
	for _, name := range []string{"OpkgTun17", "OpkgTun19"} {
		if err := p.ndms.CreateOpkgTunWithSecurityLevel(ctx, name, "старый мир", "private"); err != nil {
			t.Fatal(err)
		}
		if err := p.ndms.SetPermitAllACL(ctx, name); err != nil {
			t.Fatal(err)
		}
	}
	roletest.Converge(t, p.role, seamCfg(), proxyrt.IntentEnabled)
	for _, name := range []string{"OpkgTun17", "OpkgTun19"} {
		if p.ndms.ExitOf(name).PermitAll {
			t.Errorf("permit-all остался на %s", name)
		}
	}
}

// С тумблером ExposeToPolicies разрешение на WG-половине — часть замысла
// (policy_exit), и ресурс permit_absent его не снимает; raw-половина без
// разрешения в любом режиме.
func TestSeam_PermitAllKeptOnExitWhenExposed(t *testing.T) {
	p := newSeamParts(t)
	ctx := context.Background()
	for _, name := range []string{"OpkgTun17", "OpkgTun19"} {
		if err := p.ndms.CreateOpkgTunWithSecurityLevel(ctx, name, "старый мир", "private"); err != nil {
			t.Fatal(err)
		}
		if err := p.ndms.SetPermitAllACL(ctx, name); err != nil {
			t.Fatal(err)
		}
	}
	cfg := seamCfg()
	cfg.ExposeToPolicies = true
	roletest.Converge(t, p.role, cfg, proxyrt.IntentEnabled)
	if !p.ndms.ExitOf("OpkgTun17").PermitAll {
		t.Error("ExposeToPolicies: permit-all на WG-половине снят, хотя это выход")
	}
	if p.ndms.ExitOf("OpkgTun19").PermitAll {
		t.Error("permit-all остался на raw-половине")
	}
}

// Выключенный инстанс тоже мигрирует: интерфейсы при выключении не удаляются,
// значит остаток живёт, пока его не снимут.
func TestSeam_PermitAllResidueRemovedWhileDisabled(t *testing.T) {
	p := newSeamParts(t)
	ctx := context.Background()
	for _, name := range []string{"OpkgTun17", "OpkgTun19"} {
		if err := p.ndms.CreateOpkgTunWithSecurityLevel(ctx, name, "старый мир", "private"); err != nil {
			t.Fatal(err)
		}
		if err := p.ndms.SetPermitAllACL(ctx, name); err != nil {
			t.Fatal(err)
		}
	}
	roletest.Converge(t, p.role, seamCfg(), proxyrt.IntentDisabled)
	for _, name := range []string{"OpkgTun17", "OpkgTun19"} {
		if p.ndms.ExitOf(name).PermitAll {
			t.Errorf("permit-all остался на %s у выключенного инстанса", name)
		}
	}
}

// Выключенный инстанс С тумблером ExposeToPolicies: `policy_exit` в ведомости
// не объявляется вовсе (гейт `enabled && c.ExposeToPolicies`), значит permit
// на WG-половине уже никем не поддерживается — и остаток обязан сниматься с
// ОБЕИХ половин. Гейт исключения в permit_absent зеркалит гейт policy_exit
// именно ради этого случая: смотри он на один тумблер, WG-половина
// выключенного инстанса не мигрировала бы никогда.
func TestSeam_PermitAllResidueRemovedWhileDisabledAndExposed(t *testing.T) {
	p := newSeamParts(t)
	ctx := context.Background()
	for _, name := range []string{"OpkgTun17", "OpkgTun19"} {
		if err := p.ndms.CreateOpkgTunWithSecurityLevel(ctx, name, "старый мир", "private"); err != nil {
			t.Fatal(err)
		}
		if err := p.ndms.SetPermitAllACL(ctx, name); err != nil {
			t.Fatal(err)
		}
	}
	cfg := seamCfg()
	cfg.ExposeToPolicies = true
	roletest.Converge(t, p.role, cfg, proxyrt.IntentDisabled)
	for _, name := range []string{"OpkgTun17", "OpkgTun19"} {
		if p.ndms.ExitOf(name).PermitAll {
			t.Errorf("permit-all остался на %s у выключенного инстанса с тумблером", name)
		}
	}
}

// ── H7 ──────────────────────────────────────────────────────────

// Чужие привязки ACL доезжают до состояния ресурса ndms_access — того самого,
// что читает API, — а `_WEBADMIN_<iface>` в них не попадает: его снимает
// ресурс permit_absent, и «чужим» он не является.
//
// Проверяется результат Converge, а не поле ресурса: именно из res.States
// состояние уходит наружу, и пин по внутренностям пропустил бы потерю Attrs
// по дороге через план.
func TestSeam_NDMSAccessReportsForeignACL(t *testing.T) {
	p := newSeamParts(t)
	// Обе половины несут своё: сервер один, и чужой список на raw-половине
	// открывает абоненту ровно тот же LAN.
	p.acc.foreign = map[string][]string{
		"OpkgTun17": {"GUEST_ACL", "_WEBADMIN_OpkgTun17"},
		"OpkgTun19": {"OTHER_ACL"},
	}
	res := roletest.Converge(t, p.role, seamCfg(), proxyrt.IntentEnabled)
	if got := foreignACL(res); got != "OpkgTun17:GUEST_ACL,OpkgTun19:OTHER_ACL" {
		t.Fatalf("foreign-acl = %q, ждали OpkgTun17:GUEST_ACL,OpkgTun19:OTHER_ACL (_WEBADMIN_ отфильтрован)\n%v", got, res.States)
	}
}

// Отказ чтения по одной половине не роняет наблюдение ресурса: чужие привязки
// — сведения для показа, и потерять из-за них весь прогон нельзя. Половина, по
// которой не ответили, просто не попадает в ключ.
func TestSeam_NDMSAccessSurvivesForeignACLReadError(t *testing.T) {
	p := newSeamParts(t)
	p.acc.foreign = map[string][]string{"OpkgTun19": {"OTHER_ACL"}}
	p.acc.foreignErr = map[string]error{"OpkgTun17": errors.New("RCI недоступен")}
	res := roletest.Converge(t, p.role, seamCfg(), proxyrt.IntentEnabled)
	if got := foreignACL(res); got != "OpkgTun19:OTHER_ACL" {
		t.Fatalf("foreign-acl = %q, ждали OpkgTun19:OTHER_ACL\n%v", got, res.States)
	}
}

func foreignACL(res proxyrt.Result) string {
	for _, st := range res.States {
		if st.ID == roles.RNdmsAccess {
			return st.Attrs["foreign-acl"]
		}
	}
	return ""
}
