package netres

import (
	"context"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/proxyrt"
)

// fakeIPT — модель таблиц: chain -> список правил (строкой). Понимает -C/-I/-A/-D
// и -N/-F/-X, плюс `-S` для листинга (Output). Тесты утверждают итоговое
// состояние модели, а не форму вызовов.
type fakeIPT struct {
	chains     map[string][]string // "table/chain" -> rules
	failDelete bool                // симуляция отказа iptables на -D (M-2)
}

func newFakeIPT() *fakeIPT {
	return &fakeIPT{chains: map[string][]string{
		"filter/FORWARD": {}, "filter/INPUT": {},
		"nat/POSTROUTING": {}, "nat/PREROUTING": {},
		"mangle/PREROUTING": {}, "mangle/FORWARD": {},
	}}
}

type iptNotFound struct{}

func (iptNotFound) Error() string { return "iptables: no chain/target/match by that name" }

func (f *fakeIPT) Run(_ context.Context, args ...string) error {
	table := "filter"
	if len(args) >= 2 && args[0] == "-t" {
		table, args = args[1], args[2:]
	}
	op, chain := args[0], args[1]
	rest := args[2:]
	key := table + "/" + chain
	switch op {
	case "-N":
		f.chains[key] = []string{}
		return nil
	case "-F":
		f.chains[key] = nil
		return nil
	case "-X":
		delete(f.chains, key)
		return nil
	}
	rule := strings.Join(rest, " ")
	switch op {
	case "-C":
		for _, r := range f.chains[key] {
			if r == rule {
				return nil
			}
		}
		return iptNotFound{}
	case "-A":
		f.chains[key] = append(f.chains[key], rule)
		return nil
	case "-I":
		pos := 1
		if len(rest) > 0 && rest[0] == "1" {
			rule = strings.Join(rest[1:], " ")
		}
		_ = pos
		f.chains[key] = append([]string{rule}, f.chains[key]...)
		return nil
	case "-D":
		if f.failDelete {
			return iptNotFound{}
		}
		out := f.chains[key][:0]
		found := false
		for _, r := range f.chains[key] {
			if !found && r == rule {
				found = true
				continue
			}
			out = append(out, r)
		}
		f.chains[key] = out
		if !found {
			return iptNotFound{}
		}
		return nil
	}
	return iptNotFound{}
}

func (f *fakeIPT) Output(_ context.Context, args ...string) (string, error) {
	// Понимает только `-t T -S CHAIN` — ровно то, что зовёт listMarked.
	if len(args) != 4 || args[0] != "-t" || args[2] != "-S" {
		return "", iptNotFound{}
	}
	key := args[1] + "/" + args[3]
	var b strings.Builder
	for _, r := range f.chains[key] {
		b.WriteString("-A " + args[3] + " " + r + "\n")
	}
	return b.String(), nil
}

func (f *fakeIPT) has(key, substr string) bool {
	for _, r := range f.chains[key] {
		if r == substr {
			return true
		}
	}
	return false
}

func driveRS(t *testing.T, r proxyrt.Resource) {
	t.Helper()
	for pass := 0; pass < 5; pass++ {
		obs, err := r.Observe(context.Background())
		if err != nil {
			t.Fatalf("observe: %v", err)
		}
		steps := r.Plan(obs)
		if len(steps) == 0 {
			return
		}
		for _, s := range steps {
			if err := r.Apply(context.Background(), s); err != nil {
				t.Fatalf("apply: %v", err)
			}
		}
	}
	t.Fatal("не сошлось за 5 проходов")
}

func TestForwardRulesConverge(t *testing.T) {
	ipt := newFakeIPT()
	forwarded := 0
	rs := NewRuleSet("forward_rules", ipt, func() error { forwarded++; return nil })
	rs.SetDesired(StaticGroups(ForwardGroups([]string{"opkgtun19"})))

	driveRS(t, rs)

	if !ipt.has("filter/FORWARD", "-i opkgtun19 -j ACCEPT") ||
		!ipt.has("filter/FORWARD", "-o opkgtun19 -j ACCEPT") {
		t.Fatalf("FORWARD не приведён: %v", ipt.chains["filter/FORWARD"])
	}
	if forwarded == 0 {
		t.Fatal("ip_forward обязан включаться вместе с FORWARD")
	}
	// Идемпотентность: второй прогон пуст.
	obs, _ := rs.Observe(context.Background())
	if steps := rs.Plan(obs); len(steps) != 0 {
		t.Fatalf("повторный план не пуст: %v", steps)
	}
}

func TestMasqFullForm(t *testing.T) {
	ipt := newFakeIPT()
	rs := NewRuleSet("nat_rules", ipt, nil)
	rs.SetDesired(StaticGroups(MasqGroups([]MasqPlan{{Iface: "opkgtun19", CIDR: "10.70.0.0/16"}}, "full", "")))
	driveRS(t, rs)
	want := "-s 10.70.0.0/16 ! -o opkgtun19 -m comment --comment AWGM_WDTT -j MASQUERADE"
	if !ipt.has("nat/POSTROUTING", want) {
		t.Fatalf("full-форма MASQUERADE не та: %v", ipt.chains["nat/POSTROUTING"])
	}
}

func TestMasqInternetOnlyPinsWAN(t *testing.T) {
	// internet-only без разрешённого WAN деградировал в full-форму молча
	// (H1, PR #697) — здесь это ошибка построителя.
	ipt := newFakeIPT()
	rs := NewRuleSet("nat_rules", ipt, nil)
	rs.SetDesired(StaticGroups(MasqGroups([]MasqPlan{{Iface: "opkgtun19", CIDR: "10.70.0.0/16"}}, "internet-only", "eth3")))
	driveRS(t, rs)
	want := "-s 10.70.0.0/16 -o eth3 -m comment --comment AWGM_WDTT -j MASQUERADE"
	if !ipt.has("nat/POSTROUTING", want) {
		t.Fatalf("internet-only обязан пинить WAN: %v", ipt.chains["nat/POSTROUTING"])
	}
}

func TestMasqInternetOnlyWithoutWANRefuses(t *testing.T) {
	// Собственно случай H1 (PR #697): internet-only без разрешённого WAN.
	// Старый masqueradeMatchArgs молча возвращал full-форму `! -o iface` —
	// то есть NAT на любой egress вместо «только в выбранный WAN». Здесь
	// построитель обязан отдать пустой набор, а не деградировать.
	//
	// Отдельный тест, потому что TestMasqInternetOnlyPinsWAN всегда передаёт
	// непустой WAN и до этой ветки не доходит.
	plans := []MasqPlan{{Iface: "opkgtun19", CIDR: "10.70.0.0/16"}}
	if got := MasqGroups(plans, "internet-only", ""); len(got) != 0 {
		t.Fatalf("internet-only без WAN обязан дать пустой набор, а не %v", got)
	}

	ipt := newFakeIPT()
	rs := NewRuleSet("nat_rules", ipt, nil)
	rs.SetDesired(StaticGroups(MasqGroups(plans, "internet-only", "")))
	driveRS(t, rs)
	if len(ipt.chains["nat/POSTROUTING"]) != 0 {
		t.Fatalf("правил быть не должно: %v", ipt.chains["nat/POSTROUTING"])
	}
}

func TestRuleSetSweepsOnDesiredChange(t *testing.T) {
	// C2: смена желаемого НЕ в пустое обязана сносить прежние формы.
	cases := []struct {
		name          string
		before, after []Group
		mustDie       string // правило, обязанное исчезнуть из модели
		mustLiveKey   string // "table/chain" где проверяем want после
		want          string
	}{
		{
			// full → internet-only: старая форма `! -o` (NAT на любой egress)
			// обязана уйти — это класс H1 (PR #697).
			name:        "full→internet-only",
			before:      MasqGroups([]MasqPlan{{Iface: "opkgtun19", CIDR: "10.70.0.0/16"}}, "full", ""),
			after:       MasqGroups([]MasqPlan{{Iface: "opkgtun19", CIDR: "10.70.0.0/16"}}, "internet-only", "eth3"),
			mustDie:     "-s 10.70.0.0/16 ! -o opkgtun19 -m comment --comment AWGM_WDTT -j MASQUERADE",
			mustLiveKey: "nat/POSTROUTING",
			want:        "-s 10.70.0.0/16 -o eth3 -m comment --comment AWGM_WDTT -j MASQUERADE",
		},
		{
			// Смена выбранного WAN: старый -o eth3 обязан уйти.
			name:        "смена WAN eth3→eth2",
			before:      MasqGroups([]MasqPlan{{Iface: "opkgtun19", CIDR: "10.70.0.0/16"}}, "internet-only", "eth3"),
			after:       MasqGroups([]MasqPlan{{Iface: "opkgtun19", CIDR: "10.70.0.0/16"}}, "internet-only", "eth2"),
			mustDie:     "-s 10.70.0.0/16 -o eth3 -m comment --comment AWGM_WDTT -j MASQUERADE",
			mustLiveKey: "nat/POSTROUTING",
			want:        "-s 10.70.0.0/16 -o eth2 -m comment --comment AWGM_WDTT -j MASQUERADE",
		},
		{
			// Ренумерация интерфейса: правила на старом имени — «класс утечки,
			// который чинили в 2.17.0» (server.go:218-220 старого кода).
			name:        "смена интерфейса FORWARD",
			before:      ForwardGroups([]string{"opkgtun19"}),
			after:       ForwardGroups([]string{"opkgtun20"}),
			mustDie:     "-i opkgtun19 -j ACCEPT",
			mustLiveKey: "filter/FORWARD",
			want:        "-i opkgtun20 -j ACCEPT",
		},
		{
			// RelayMode wg→raw: DNAT :53 на WG-интерфейсе обязан уйти.
			name: "RelayMode wg→raw снимает WG-DNAT",
			before: DNSGroups([]DNSHijack{
				{Iface: "opkgtun19", Gateway: "10.70.66.1"},
				{Iface: "opkgtun17", Gateway: "10.66.0.1"},
			}),
			after:       DNSGroups([]DNSHijack{{Iface: "opkgtun19", Gateway: "10.70.66.1"}}),
			mustDie:     "-i opkgtun17 -p udp --dport 53 -j DNAT --to-destination 10.66.0.1:53",
			mustLiveKey: "nat/PREROUTING",
			want:        "-i opkgtun19 -p udp --dport 53 -j DNAT --to-destination 10.70.66.1:53",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ipt := newFakeIPT()
			rs := NewRuleSet("nat_rules", ipt, nil)
			rs.SetDesired(StaticGroups(c.before))
			driveRS(t, rs)
			rs.SetDesired(StaticGroups(c.after))
			driveRS(t, rs)
			for key, rules := range ipt.chains {
				for _, r := range rules {
					if r == c.mustDie {
						t.Fatalf("правило прежнего желаемого пережило смену (%s): %v", key, r)
					}
				}
			}
			if !ipt.has(c.mustLiveKey, c.want) {
				t.Fatalf("новое желаемое не приведено: %v", ipt.chains[c.mustLiveKey])
			}
		})
	}
}

func TestRuleSetAdoptsStaleMarkedAfterDaemonRestart(t *testing.T) {
	// I-1: разность прогонов — память процесса. Правило прежней формы,
	// поставленное ПРЕЖНИМ запуском демона (конфиг сменился, пока демон
	// лежал), обязано быть усыновлено по метке и снесено — паритет
	// flushEntwareMasquerade (entware_nat_linux.go:374-389).
	ipt := newFakeIPT()
	ipt.chains["nat/POSTROUTING"] = []string{
		// full-форма от прежней жизни демона.
		"-s 10.70.0.0/16 ! -o opkgtun19 -m comment --comment AWGM_WDTT -j MASQUERADE",
		// чужое правило без нашей метки — трогать нельзя.
		"-s 192.168.1.0/24 -j MASQUERADE",
	}
	// СВЕЖИЙ RuleSet = рестарт демона: last/doomed пусты.
	rs := NewRuleSet("nat_rules", ipt, nil)
	rs.SetDesired(StaticGroups(MasqGroups(
		[]MasqPlan{{Iface: "opkgtun19", CIDR: "10.70.0.0/16"}}, "internet-only", "eth3")))
	driveRS(t, rs)

	for _, r := range ipt.chains["nat/POSTROUTING"] {
		if strings.Contains(r, "! -o opkgtun19") {
			t.Fatalf("помеченное правило прежнего запуска пережило рестарт: %v", ipt.chains["nat/POSTROUTING"])
		}
	}
	if !ipt.has("nat/POSTROUTING", "-s 192.168.1.0/24 -j MASQUERADE") {
		t.Fatal("чужое правило без метки снесено — усыновление вышло за владение")
	}
	if !ipt.has("nat/POSTROUTING", "-s 10.70.0.0/16 -o eth3 -m comment --comment AWGM_WDTT -j MASQUERADE") {
		t.Fatalf("новое желаемое не приведено: %v", ipt.chains["nat/POSTROUTING"])
	}
}

func TestRuleSetSweepKeepsRuleOnFailedDelete(t *testing.T) {
	// M-2: неудавшийся снос не выбрасывает правило из ведомости.
	ipt := newFakeIPT()
	rs := NewRuleSet("forward_rules", ipt, nil)
	rs.SetDesired(StaticGroups(ForwardGroups([]string{"opkgtun19"})))
	driveRS(t, rs)
	rs.SetDesired(StaticGroups(ForwardGroups([]string{"opkgtun20"})))

	ipt.failDelete = true
	obs, err := rs.Observe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	steps := rs.Plan(obs)
	failed := false
	for _, st := range steps {
		if err := rs.Apply(context.Background(), st); err != nil {
			failed = true
		}
	}
	if !failed {
		t.Fatal("отказ iptables обязан доехать ошибкой шага")
	}
	ipt.failDelete = false
	driveRS(t, rs)
	if ipt.has("filter/FORWARD", "-i opkgtun19 -j ACCEPT") {
		t.Fatal("правило потеряно из ведомости после неудачного сноса")
	}
}

func TestPolicyMarkPairAllOrNone(t *testing.T) {
	// Пара CONNMARK+MARK: частичное состояние пересобирается ЦЕЛИКОМ —
	// одиночная довставка инвертирует порядок (F3, PR #697, a0066f9b).
	//
	// Оба частичных состояния обязаны проверяться. Фикстура плана оставляла
	// живым только CONNMARK, а это половина, на которой инверсия НЕ видна:
	// довставка MARK на позицию 1 даёт правильный порядок сама собой, и
	// мутант «убрать пересборку» выживает. Инверсию обнажает зеркальный
	// случай — уцелел MARK, довставляется CONNMARK на позицию 1.
	const (
		markRule     = "-i opkgtun19 -j MARK --set-xmark 0xffffd00/0xffffffff"
		connmarkRule = "-i opkgtun19 -j CONNMARK --save-mark --nfmask 0xffffffff --ctmask 0xffffffff"
	)
	for _, c := range []struct {
		name     string
		survivor string
	}{
		{name: "уцелел CONNMARK", survivor: connmarkRule},
		{name: "уцелел MARK", survivor: markRule},
	} {
		t.Run(c.name, func(t *testing.T) {
			ipt := newFakeIPT()
			// Половина пары уже стоит.
			ipt.chains["mangle/PREROUTING"] = []string{c.survivor}
			rs := NewRuleSet("nat_rules", ipt, nil)
			rs.SetDesired(StaticGroups([]Group{PolicyMarkGroup("opkgtun19", "0xffffd00")}))
			driveRS(t, rs)

			got := ipt.chains["mangle/PREROUTING"]
			if len(got) != 2 || got[0] != markRule || got[1] != connmarkRule {
				t.Fatalf("итоговый порядок обязан быть MARK, CONNMARK: %v", got)
			}
		})
	}
}

func TestMSSClampBuildsChain(t *testing.T) {
	ipt := newFakeIPT()
	m := NewMSSClamp("mss_clamp", ipt)
	m.SetDesired([]string{"10.70.0.0/16"})
	driveRS(t, m)

	rules := ipt.chains["mangle/awgm_wdtt_mangle"]
	if len(rules) != 2 ||
		!strings.Contains(rules[0], "-s 10.70.0.0/16") ||
		!strings.Contains(rules[1], "-d 10.70.0.0/16") {
		t.Fatalf("clamp-правила не те: %v", rules)
	}
	if !ipt.has("mangle/FORWARD", "-j awgm_wdtt_mangle") {
		t.Fatal("jump в свою цепочку не поставлен")
	}
}

func TestHookScriptRendersFromSameRules(t *testing.T) {
	// Хук — ЧЕТВЁРТЫЙ рендер тех же Rule: если правило есть в декларации,
	// его форма обязана дословно попасть в скрипт (одно описание — кандидат №3).
	groups := append(ForwardGroups([]string{"opkgtun19"}),
		MasqGroups([]MasqPlan{{Iface: "opkgtun19", CIDR: "10.70.0.0/16"}}, "full", "")...)
	script := HookScript(groups)

	for _, want := range []string{
		"[ \"$type\" = \"ip6tables\" ] && exit 0",
		"has_if \"opkgtun19\"",
		"run -C FORWARD -i \"opkgtun19\" -j ACCEPT || run -I FORWARD 1 -i \"opkgtun19\" -j ACCEPT",
		"run -t nat -C POSTROUTING -s 10.70.0.0/16 ! -o \"opkgtun19\" -m comment --comment AWGM_WDTT -j MASQUERADE",
		"case \"$table\" in",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("в хуке нет %q:\n%s", want, script)
		}
	}
}

func TestHookResourceWritesAndRefreshes(t *testing.T) {
	dir := t.TempDir()
	var ran []string
	h := NewHook("netfilter_hook", dir+"/61-awgm-wdtt-forward.sh",
		func(_ context.Context, path, table string) error {
			ran = append(ran, table)
			return nil
		})
	h.SetDesired(StaticGroups(ForwardGroups([]string{"opkgtun19"})))

	driveRS(t, h)

	if len(ran) != 3 { // filter, nat, mangle — как ensureWdttNetfilterHook
		t.Fatalf("хук обязан прогоняться по трём таблицам: %v", ran)
	}
	// Смена декларации меняет файл; та же — не трогает.
	obs, _ := h.Observe(context.Background())
	if steps := h.Plan(obs); len(steps) != 0 {
		t.Fatalf("без изменений шагов нет: %v", steps)
	}
	h.SetDesired(StaticGroups(ForwardGroups([]string{"opkgtun20"})))
	obs, _ = h.Observe(context.Background())
	if steps := h.Plan(obs); len(steps) == 0 {
		t.Fatal("смена декларации обязана переписать хук")
	}
}

func TestHookDisabledRemovesFile(t *testing.T) {
	dir := t.TempDir()
	h := NewHook("netfilter_hook", dir+"/61-awgm-wdtt-forward.sh",
		func(context.Context, string, string) error { return nil })
	h.SetDesired(StaticGroups(ForwardGroups([]string{"opkgtun19"})))
	driveRS(t, h)
	h.SetDesired(nil) // disabled / NAT none: правил нет — хука нет
	driveRS(t, h)
	obs, _ := h.Observe(context.Background())
	if obs.Exists {
		t.Fatal("пустая декларация обязана снимать хук")
	}
}

// fakeFW — модель живых managed-правил listenfirewall: Reconcile приводит
// набор целиком (как прод), Managed листает живое.
type fakeFW struct{ open map[string]PortSpec }

func (f *fakeFW) Managed(context.Context) ([]PortSpec, error) {
	var out []PortSpec
	for _, s := range f.open {
		out = append(out, s)
	}
	return out, nil
}

func (f *fakeFW) Reconcile(_ context.Context, desired []PortSpec) error {
	next := map[string]PortSpec{}
	for _, s := range desired {
		next[portKey(s)] = s
	}
	f.open = next
	return nil
}

func (f *fakeFW) has(port int, proto string) bool {
	_, ok := f.open[portKey(PortSpec{Port: port, Proto: proto})]
	return ok
}

func TestInputPortConvergesAndCloses(t *testing.T) {
	fw := &fakeFW{open: map[string]PortSpec{}}
	p := NewInputPort("input_port", fw)
	p.SetDesired([]PortSpec{{Port: 56000, Proto: "udp"}, {Port: 56003, Proto: "udp"}})
	driveRS(t, p)
	if !fw.has(56000, "udp") || !fw.has(56003, "udp") {
		t.Fatal("порты не открыты")
	}
	p.SetDesired(nil)
	driveRS(t, p)
	if fw.has(56000, "udp") {
		t.Fatal("снятое желаемое обязано закрывать порт")
	}
}

func TestInputPortClosesOldPortOnChange(t *testing.T) {
	// C2: смена WAN-порта обязана ЗАКРЫВАТЬ прежний — его иначе вечно
	// восстанавливает собственный хук listenfirewall (62-awgm-listen-ports.sh),
	// то есть это постоянная дыра, а не «до первого rewrite».
	fw := &fakeFW{open: map[string]PortSpec{}}
	p := NewInputPort("input_port", fw)
	p.SetDesired([]PortSpec{{Port: 56000, Proto: "udp"}})
	driveRS(t, p)
	p.SetDesired([]PortSpec{{Port: 56100, Proto: "udp"}})
	driveRS(t, p)
	if fw.has(56000, "udp") {
		t.Fatal("прежний порт остался открыт после смены listen")
	}
	if !fw.has(56100, "udp") {
		t.Fatal("новый порт не открыт")
	}
}

func TestInputPortHealsStalePortAfterDaemonRestart(t *testing.T) {
	// I-1: порт, открытый прежним запуском демона (конфиг сменился, пока
	// демон лежал), виден через Managed и закрывается Reconcile — ведомость
	// живёт в правилах, не в памяти процесса.
	fw := &fakeFW{open: map[string]PortSpec{
		portKey(PortSpec{Port: 56000, Proto: "udp"}): {Port: 56000, Proto: "udp"},
	}}
	p := NewInputPort("input_port", fw) // свежий ресурс = рестарт
	p.SetDesired([]PortSpec{{Port: 56100, Proto: "udp"}})
	driveRS(t, p)
	if fw.has(56000, "udp") {
		t.Fatal("протухший порт прежнего запуска пережил реконсиляцию")
	}
	if !fw.has(56100, "udp") {
		t.Fatal("новый порт не открыт")
	}
}
