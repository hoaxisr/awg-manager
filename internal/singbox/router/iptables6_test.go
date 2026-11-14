package router

import (
	"context"
	"errors"
	"os/exec"
	"reflect"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// v6Exec — записывающая реализация v6Backend.
type v6Exec struct {
	restores []string
	ip6t     [][]string
	ip       [][]string
	// dump — что отвечает `ip6tables -S`.
	dump       string
	dumpErr    error
	restoreErr error
	// persistErr — отказ записи файлов правил (PersistRules).
	persistErr error
	evicted    int
	hookWrites int
	cleanups   int
	// rulesWrites — сколько раз записаны файлы правил перехвата.
	rulesWrites int

	blackholeWrites   int
	blackholeCleanups int

	// events — порядок обращений к restore и к записи файлов.
	events []string
	// onRestore, если задан, подменяет запись restore: тесты снимают в нём
	// состояние окружения на момент применения правил.
	onRestore func(in string) error
	// onRunOutput зовётся на каждом `ip6tables -S`.
	onRunOutput func()
}

func (fe *v6Exec) Restore(_ context.Context, in string) error {
	fe.events = append(fe.events, "restore")
	if fe.onRestore != nil {
		return fe.onRestore(in)
	}
	fe.restores = append(fe.restores, in)
	return fe.restoreErr
}

func (fe *v6Exec) Run(_ context.Context, args ...string) error {
	fe.ip6t = append(fe.ip6t, args)
	return nil
}

func (fe *v6Exec) RunOutput(context.Context, ...string) (string, error) {
	if fe.onRunOutput != nil {
		fe.onRunOutput()
	}
	return fe.dump, fe.dumpErr
}

func (fe *v6Exec) PersistRules(_, _, _ string) error {
	fe.events = append(fe.events, "rules")
	if fe.persistErr != nil {
		return fe.persistErr
	}
	fe.rulesWrites++
	return nil
}

func (fe *v6Exec) PersistHook() error {
	fe.events = append(fe.events, "hook")
	fe.hookWrites++
	return nil
}

func (fe *v6Exec) RemoveFiles() { fe.cleanups++ }

func (fe *v6Exec) CleanFlows(context.Context) {
	fe.events = append(fe.events, "clean")
	fe.evicted++
}

func (fe *v6Exec) PersistBlackhole(string) error {
	fe.events = append(fe.events, "blackhole")
	fe.blackholeWrites++
	return nil
}

func (fe *v6Exec) RemoveBlackholeFile() { fe.blackholeCleanups++ }

// nopV6 — v6Backend, который ничего не делает и ничего не находит: для
// тестовых IPTables, которым v6-слой не интересен. Пустой ответ `-S` значит
// «наших цепочек нет».
type nopV6 struct{}

func (nopV6) Restore(context.Context, string) error                { return nil }
func (nopV6) Run(context.Context, ...string) error                 { return nil }
func (nopV6) RunOutput(context.Context, ...string) (string, error) { return "", nil }
func (nopV6) PersistRules(_, _, _ string) error                    { return nil }
func (nopV6) PersistHook() error                                   { return nil }
func (nopV6) RemoveFiles()                                         {}
func (nopV6) CleanFlows(context.Context)                           {}
func (nopV6) PersistBlackhole(string) error                        { return nil }
func (nopV6) RemoveBlackholeFile()                                 {}

// Системная сторона слоя — один интерфейс. Неполная реализация не
// компилируется, поэтому проверять «подключены ли зависимости» во время
// работы нечем и незачем.
var (
	_ v6Backend = systemV6{}
	_ v6Backend = (*v6Exec)(nil)
	_ v6Backend = nopV6{}
)

func newV6IPTables(fe *v6Exec) *IPTables {
	it := newStubIPTables(func(context.Context, string) error { return nil })
	it.v6 = fe
	it.runIP = func(_ context.Context, args ...string) error {
		fe.ip = append(fe.ip, args)
		// `ip -6 rule del` — одно удаление, затем ENOENT: иначе цикл drain
		// честно отработал бы все свои проходы.
		if len(args) >= 3 && args[0] == "-6" && args[1] == "rule" && args[2] == "del" {
			n := 0
			for _, c := range fe.ip {
				if len(c) >= 3 && c[0] == "-6" && c[1] == "rule" && c[2] == "del" {
					n++
				}
			}
			if n > 1 {
				return errENOENT
			}
		}
		return nil
	}
	return it
}

func v6JumpsDump() string {
	return "-P PREROUTING ACCEPT\n" +
		"-N " + Chain6Name + "\n" +
		"-N " + Redirect6Chain + "\n" +
		"-A PREROUTING -i br0 -m mark --mark 0xffffaab -m conntrack --ctdir ORIGINAL -j " + Chain6Name + "\n" +
		"-A PREROUTING -i br0 -m mark --mark 0xffffaab -j " + Redirect6Chain + "\n"
}

func testV6Spec() V6Spec {
	return V6Spec{
		PolicyMark: "0xffffaab",
		Bridges:    []V6Bridge{{Name: "br0", TProxyPort: TProxy6PortBase}},
		LocalCIDRs: []string{"2001:db8:1::/64", "2001:db8:1::1/128", "2001:db8:ffff::2/128"},
	}
}

func indexOf(t *testing.T, s, sub string) int {
	t.Helper()
	i := strings.Index(s, sub)
	if i < 0 {
		t.Fatalf("в правилах нет %q:\n%s", sub, s)
	}
	return i
}

// Порядок в цепочке mangle несёт смысл и повторяет v4: перехват DNS стоит ВЫШЕ
// исключений по адресам (иначе запрос на link-local адрес роутера ушёл бы в
// RETURN по fe80::/10 — ровно та утечка DNS, ради которой слой и появился), а
// исключения — выше catch-all.
func TestBuildMangleRestoreInput6_Order(t *testing.T) {
	spec := testV6Spec()
	spec.BypassUDPPorts = []PortRange{{From: 123, To: 123}}
	got := buildMangleRestoreInput6(spec)

	bypassPort := indexOf(t, got, "-A AWGM6-TPROXY -p udp --dport 123 -j RETURN")
	dns := indexOf(t, got, "-A AWGM6-TPROXY -i br0 -p udp --dport 53 -j TPROXY --on-port 51321 --on-ip ::1 --tproxy-mark 0x1")
	linkLocal := indexOf(t, got, "-A AWGM6-TPROXY -d fe80::/10 -j RETURN")
	lan := indexOf(t, got, "-A AWGM6-TPROXY -d 2001:db8:1::/64 -j RETURN")
	catchAll := indexOf(t, got, "-A AWGM6-TPROXY -i br0 -p udp -j TPROXY --on-port 51321 --on-ip ::1 --tproxy-mark 0x1")
	jump := indexOf(t, got, "-A PREROUTING -i br0 -m mark --mark 0xffffaab -m conntrack --ctdir ORIGINAL -j AWGM6-TPROXY")

	if !(bypassPort < dns && dns < linkLocal && linkLocal < lan && lan < catchAll && catchAll < jump) {
		t.Errorf("нарушен порядок правил:\n%s", got)
	}
	if !strings.HasPrefix(got, "*mangle\n:AWGM6-TPROXY - [0:0]\n") || !strings.HasSuffix(got, "COMMIT\n") {
		t.Errorf("блоб обязан начинаться с объявления цепочки и кончаться COMMIT:\n%s", got)
	}
}

// В nat фильтра направления нет (таблицу проходит только первый пакет
// соединения), а REDIRECT ведёт на порт TCP-инбаунда.
func TestBuildNatRestoreInput6(t *testing.T) {
	spec := testV6Spec()
	spec.BypassTCPPorts = []PortRange{{From: 22, To: 22}}
	got := buildNatRestoreInput6(spec)

	excl := indexOf(t, got, "-A AWGM6-REDIRECT -d fc00::/7 -j RETURN")
	router := indexOf(t, got, "-A AWGM6-REDIRECT -d 2001:db8:ffff::2/128 -j RETURN")
	port := indexOf(t, got, "-A AWGM6-REDIRECT -p tcp --dport 22 -j RETURN")
	catchAll := indexOf(t, got, "-A AWGM6-REDIRECT -p tcp -j REDIRECT --to-ports 51273")
	jump := indexOf(t, got, "-A PREROUTING -i br0 -m mark --mark 0xffffaab -j AWGM6-REDIRECT")
	if !(excl < router && router < port && port < catchAll && catchAll < jump) {
		t.Errorf("нарушен порядок правил:\n%s", got)
	}
	if strings.Contains(got, "ctdir") {
		t.Errorf("в nat фильтр направления не нужен:\n%s", got)
	}
}

// Режим «все устройства» — как в v4: один джамп без интерфейса, в перехват
// попадает трафик с любого интерфейса. Мосты идут в свои инбаунды (им нужна
// привязка ради link-local DNS), всё остальное — в общий. Аплинки отсекаются
// первым правилом цепочки: входящие из интернета соединения к устройствам за
// LAN в sing-box идти не должны.
func TestBuildRestoreInput6_MatchAll(t *testing.T) {
	spec := V6Spec{
		MatchAll: true,
		Bridges: []V6Bridge{
			{Name: "br0", TProxyPort: TProxy6PortBase},
			{Name: "br1", TProxyPort: TProxy6PortBase + 1},
		},
		Uplinks: []string{"ppp0"},
	}
	mangle := buildMangleRestoreInput6(spec)
	// lo отсекается самым первым: иначе собственный DNS роутера по loopback
	// заворачивался бы в sing-box и зацикливался.
	if !strings.HasPrefix(mangle, "*mangle\n:AWGM6-TPROXY - [0:0]\n-A AWGM6-TPROXY -i lo -j RETURN\n") {
		t.Errorf("mangle: первым правилом обязан быть RETURN для lo:\n%s", mangle)
	}
	uplink := indexOf(t, mangle, "-A AWGM6-TPROXY -i ppp0 -j RETURN")
	dnsBr1 := indexOf(t, mangle, "-A AWGM6-TPROXY -i br1 -p udp --dport 53 -j TPROXY --on-port 51322 --on-ip ::1")
	dnsAny := indexOf(t, mangle, "-A AWGM6-TPROXY -p udp --dport 53 -j TPROXY --on-port 51274 --on-ip ::1")
	excl := indexOf(t, mangle, "-A AWGM6-TPROXY -d fe80::/10 -j RETURN")
	allBr0 := indexOf(t, mangle, "-A AWGM6-TPROXY -i br0 -p udp -j TPROXY --on-port 51321")
	allAny := indexOf(t, mangle, "-A AWGM6-TPROXY -p udp -j TPROXY --on-port 51274")
	jump := indexOf(t, mangle, "-A PREROUTING -m conntrack --ctdir ORIGINAL -j AWGM6-TPROXY")
	if !(uplink < dnsBr1 && dnsBr1 < dnsAny && dnsAny < excl && excl < allBr0 && allBr0 < allAny && allAny < jump) {
		t.Errorf("mangle: нарушен порядок правил:\n%s", mangle)
	}

	nat := buildNatRestoreInput6(spec)
	for _, want := range []string{
		"-A AWGM6-REDIRECT -i lo -j RETURN",
		"-A AWGM6-REDIRECT -i ppp0 -j RETURN",
		"-A PREROUTING -j AWGM6-REDIRECT",
	} {
		if !strings.Contains(nat, want) {
			t.Errorf("nat: нет %q:\n%s", want, nat)
		}
	}
	got := mangle + nat
	if strings.Contains(got, "--mark 0x") && strings.Contains(got, "-m mark") {
		t.Errorf("в режиме «все устройства» метки политики быть не должно:\n%s", got)
	}
	if strings.Count(got, "-A PREROUTING") != 2 {
		t.Errorf("в режиме «все устройства» ровно по одному джампу на таблицу:\n%s", got)
	}
}

// Режим политики без метки и роутер без мостов с IPv6 — джампов нет вовсе.
func TestBuildRestoreInput6_NoJumps(t *testing.T) {
	for name, spec := range map[string]V6Spec{
		"политика без метки": {Bridges: []V6Bridge{{Name: "br0", TProxyPort: TProxy6PortBase}}},
		"нет мостов":         {PolicyMark: "0xffffaab"},
	} {
		if got := buildRestoreInput6(spec); strings.Contains(got, "-A PREROUTING") {
			t.Errorf("%s: джампов быть не должно:\n%s", name, got)
		}
		if v6HasJumps(spec) {
			t.Errorf("%s: v6HasJumps обязан быть false", name)
		}
	}
}

func TestEqualV6Spec(t *testing.T) {
	a, b := testV6Spec(), testV6Spec()
	if !equalV6Spec(&a, &b) {
		t.Error("одинаковые спеки обязаны быть равны")
	}
	b.LocalCIDRs = []string{"2001:db8:2::/64"}
	if equalV6Spec(&a, &b) {
		t.Error("смена префикса LAN обязана менять спек")
	}
	if equalV6Spec(nil, &a) || !equalV6Spec(nil, nil) {
		t.Error("nil равен только nil")
	}
}

func TestV6BridgePort(t *testing.T) {
	cases := []struct {
		name string
		port int
		ok   bool
	}{
		{"br0", TProxy6PortBase, true},
		{"br15", TProxy6PortBase + 15, true},
		{"br16", 0, false},
		{"br01", 0, false},
		{"br", 0, false},
		{"br-1", 0, false},
		{"eth0", 0, false},
	}
	for _, c := range cases {
		port, ok := v6BridgePort(c.name)
		if port != c.port || ok != c.ok {
			t.Errorf("v6BridgePort(%q) = %d, %v; want %d, %v", c.name, port, ok, c.port, c.ok)
		}
	}
}

// Образец /proc/net/if_inet6: формат строк и набор интерфейсов — как на
// роутере, на котором слой проверялся; ВСЕ адреса синтетические
// (документационные префиксы, link-local от документационных MAC
// 00:00:5e:00:53:xx). br0 — мост, остальное — нет.
const ifInet6Sample = `00000000000000000000000000000001 01 80 10 80       lo
fe8000000000000002005efffe005301 0b 40 20 80      br0
20010db8000100000000000000000001 0b 40 00 00      br0
fd000000000000010000000000000001 0b 40 00 00      br0
fe8000000000000002005efffe005304 1c 0a 20 80     ppp0
20010db8ffff00000000000000000002 1c 40 00 00     ppp0
fc000000000000020000000000000001 2a 80 00 80     t2s0
fe8000000000000002005efffe005302 05 40 20 80     eth2
мусорная строка
`

func TestParseIfInet6(t *testing.T) {
	topo := parseIfInet6(ifInet6Sample, func(dev string) bool { return dev == "br0" })

	if !reflect.DeepEqual(topo.Bridges, []string{"br0"}) {
		t.Errorf("Bridges = %v, want [br0]", topo.Bridges)
	}
	// Префикс моста и адреса роутера; ULA (fd00…, fc00…) покрыты fc00::/7 и в
	// список не идут; префикс WAN (не мост) не исключается — чужие хосты в нём
	// обычный интернет.
	want := []string{"2001:db8:1::/64", "2001:db8:1::1/128", "2001:db8:ffff::2/128"}
	if !reflect.DeepEqual(topo.LocalCIDRs, want) {
		t.Errorf("LocalCIDRs = %v, want %v", topo.LocalCIDRs, want)
	}
}

// Мост без link-local адреса — IPv6 на нём не поднят, в перехват не идёт.
func TestParseIfInet6_BridgeWithoutLinkLocal(t *testing.T) {
	data := "20010db8000100000000000000000001 0b 40 00 00      br0\n"
	topo := parseIfInet6(data, func(string) bool { return true })
	if len(topo.Bridges) != 0 {
		t.Errorf("Bridges = %v, want пусто", topo.Bridges)
	}
}

func TestBuildV6Spec_SkipsUnmappableBridges(t *testing.T) {
	topo := v6Topology{Bridges: []string{"br0", "br99"}}
	spec, skipped, _ := buildV6Spec(storage.SingboxRouterSettings{}, "0xffffaab", true, topo, nil, nil)
	if len(spec.Bridges) != 1 || spec.Bridges[0].Name != "br0" {
		t.Errorf("Bridges = %v, want только br0", spec.Bridges)
	}
	if !reflect.DeepEqual(skipped, []string{"br99"}) {
		t.Errorf("skipped = %v, want [br99]", skipped)
	}
	if spec.MatchAll {
		t.Error("policyMode=true обязан давать MatchAll=false")
	}
}

func TestEnsureV6Inbounds(t *testing.T) {
	base := []Inbound{{Type: "tproxy", Tag: "tproxy-in", Listen: "127.0.0.1", ListenPort: TPROXYPort}}
	spec := V6Spec{PolicyMark: "0xffffaab", Bridges: []V6Bridge{{Name: "br0", TProxyPort: TProxy6PortBase}}}

	got, changed := ensureV6Inbounds(base, spec, true, "", 0)
	if !changed || len(got) != 3 {
		t.Fatalf("включение: changed=%v, len=%d, want true, 3", changed, len(got))
	}
	udp := got[2]
	if udp.Tag != "tproxy6-in-br0" || udp.Listen != "::1" || udp.ListenPort != TProxy6PortBase ||
		udp.BindInterface != "br0" || udp.Network != "udp" || udp.UDPTimeout != DefaultUDPTimeout {
		t.Errorf("UDP-инбаунд не канонический: %+v", udp)
	}
	tcp := got[1]
	if tcp.Tag != "redirect6-in" || tcp.Listen != "::" || tcp.ListenPort != Redirect6Port || tcp.Type != "redirect" {
		t.Errorf("TCP-инбаунд не канонический: %+v", tcp)
	}

	// Идемпотентность: на сведённом конфиге изменений нет.
	if _, changed := ensureV6Inbounds(got, spec, true, "", 0); changed {
		t.Error("повторный вызов не должен ничего менять")
	}
	// Смена таймаута доезжает.
	if _, changed := ensureV6Inbounds(got, spec, true, "10m0s", 0); !changed {
		t.Error("смена udp_timeout обязана менять инбаунды")
	}
	// Выключение сносит только свои инбаунды.
	off, changed := ensureV6Inbounds(got, spec, false, "", 0)
	if !changed || !reflect.DeepEqual(off, base) {
		t.Errorf("выключение: changed=%v, got %+v, want %+v", changed, off, base)
	}
	if _, changed := ensureV6Inbounds(base, V6Spec{}, false, "", 0); changed {
		t.Error("выключенный слой на чистом конфиге не должен ничего менять")
	}
}

// Переход tproxy → policy-tun переиспользует слот 20: инбаунды v6-слоя обязаны
// уйти вместе с tproxy-in/redirect-in, а QoS-инбаунды — остаться.
func TestEnsurePolicyTunInbound_DropsV6Inbounds(t *testing.T) {
	in := []Inbound{
		{Type: "redirect", Tag: redirect6Tag},
		{Type: "tproxy", Tag: tproxy6Tag("br0")},
		{Type: "tproxy", Tag: qosTProxyTag(46)},
	}
	out := ensurePolicyTunInbound(in, PolicyTunInboundSpec{Iface: "opkgtun0", TunAddr4: "172.19.0.1/30", MTU: 1500})
	qosKept := false
	for _, i := range out {
		if isV6InboundTag(i.Tag) {
			t.Errorf("инбаунд v6-слоя %q не должен пережить переход в policy-tun", i.Tag)
		}
		if i.Tag == qosTProxyTag(46) {
			qosKept = true
		}
	}
	if !qosKept {
		t.Error("QoS-инбаунд обязан остаться")
	}
}

// Хук исполняется на живом роутере при каждой перестройке файрвола —
// синтаксическая ошибка ломала бы каждую.
func TestNetfilter6HookScript_ValidShell(t *testing.T) {
	script := netfilter6HookScript()

	cmd := exec.Command("sh", "-n")
	cmd.Stdin = strings.NewReader(script)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("сгенерированный хук — невалидный sh: %v\n%s", err, out)
	}
	for _, want := range []string{
		// События v4 хук пропускает, пустой $type — нет.
		`[ "$type" = "iptables" ] && exit 0`,
		"table_ok mangle " + Chain6Name,
		"table_ok nat " + Redirect6Chain,
		"/opt/sbin/ip6tables-restore --noflush",
		"-6 rule add fwmark 0x1 table 100 priority 1100",
		"-6 route add local ::/0 dev lo table 100",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("в хуке нет %q:\n%s", want, script)
		}
	}
}

// Хук — зеркало v4: при живом sing-box снимает блокировку и возвращает
// перехват, при мёртвом — возвращает блокировку (если её файл есть), а
// перехват в мёртвый порт НЕ восстанавливает.
func TestNetfilter6HookScript_AliveAndDeadBranches(t *testing.T) {
	script := netfilter6HookScript()
	alive := indexOf(t, script, "if pidof awgm-box >/dev/null 2>&1; then")
	scrubBH := indexOf(t, script, "scrub_jumps mangle "+Blackhole6Chain)
	restore := indexOf(t, script, "ip6tables-restore --noflush < \"/opt/etc/awg-manager/singbox/router-netfilter6-mangle.rules\"")
	dead := indexOf(t, script, "\nelse\n")
	gate := indexOf(t, script, "[ -f \"/opt/etc/awg-manager/singbox/router-blackhole6.rules\" ] || exit 0")
	reassert := indexOf(t, script, "ip6tables-restore --noflush < \"/opt/etc/awg-manager/singbox/router-blackhole6.rules\"")
	if !(alive < scrubBH && scrubBH < restore && restore < dead && dead < gate && gate < reassert) {
		t.Errorf("ветки хука перепутаны:\n%s", script)
	}
	if !strings.Contains(script, "grep -qE -- '-[jg] "+Blackhole6Chain+"($| )'") {
		t.Error("DEAD-ветка обязана проверять джамп блокировки перед восстановлением — иначе restore добавит второй")
	}
}

func hasCall(calls [][]string, want ...string) bool {
	for _, c := range calls {
		if reflect.DeepEqual(c, want) {
			return true
		}
	}
	return false
}

func TestInstall6(t *testing.T) {
	fe := &v6Exec{dump: v6JumpsDump()}
	it := newV6IPTables(fe)
	spec := testV6Spec()

	if err := it.Install6(context.Background(), spec); err != nil {
		t.Fatalf("Install6: %v", err)
	}
	if len(fe.restores) != 1 || fe.restores[0] != buildRestoreInput6(spec) {
		t.Fatalf("restore обязан получить ровно рендер спека, got %d вызов(ов)", len(fe.restores))
	}
	// Прежние джампы сняты ДО restore: иначе --noflush добавил бы вторые.
	if !hasCall(fe.ip6t, "-t", "mangle", "-D", "PREROUTING", "-i", "br0", "-m", "mark", "--mark", "0xffffaab",
		"-m", "conntrack", "--ctdir", "ORIGINAL", "-j", Chain6Name) {
		t.Errorf("прежний джамп mangle не снят: %v", fe.ip6t)
	}
	if !hasCall(fe.ip6t, "-t", "nat", "-D", "PREROUTING", "-i", "br0", "-m", "mark", "--mark", "0xffffaab", "-j", Redirect6Chain) {
		t.Errorf("прежний джамп nat не снят: %v", fe.ip6t)
	}
	if !hasCall(fe.ip, "-6", "rule", "add", "fwmark", "0x1", "table", "100", "priority", "1100") {
		t.Errorf("нет ip -6 rule add: %v", fe.ip)
	}
	if !hasCall(fe.ip, "-6", "route", "add", "local", "::/0", "dev", "lo", "table", "100") {
		t.Errorf("нет ip -6 route add: %v", fe.ip)
	}
	if fe.rulesWrites != 1 || fe.hookWrites != 1 || fe.evicted != 1 {
		t.Errorf("rulesWrites=%d, hookWrites=%d, evicted=%d, want 1, 1, 1", fe.rulesWrites, fe.hookWrites, fe.evicted)
	}
	// Файлы правил и хук — только после успешного restore; чистка потоков —
	// последней, когда правила уже стоят.
	if want := []string{"restore", "rules", "hook", "clean"}; !reflect.DeepEqual(fe.events, want) {
		t.Errorf("порядок установки: %v, want %v", fe.events, want)
	}
}

// Отвергнутый ядром спек не должен попасть на диск: хук применяет файлы
// правил на каждой перестройке файрвола и воспроизводил бы тот же отказ.
func TestInstall6_RestoreError(t *testing.T) {
	fe := &v6Exec{restoreErr: errors.New("line 3 failed")}
	it := newV6IPTables(fe)
	if err := it.Install6(context.Background(), testV6Spec()); err == nil {
		t.Fatal("ошибка ip6tables-restore обязана уйти наверх")
	}
	if len(fe.ip) != 0 {
		t.Errorf("после провала restore правило и маршрут ставить нельзя: %v", fe.ip)
	}
	if fe.rulesWrites != 0 || fe.hookWrites != 0 || fe.evicted != 0 {
		t.Errorf("после провала restore на диск писать нельзя: rules=%d hook=%d clean=%d",
			fe.rulesWrites, fe.hookWrites, fe.evicted)
	}
}

// Отказ записи файлов правил — тоже ошибка установки: без них хук не вернёт
// перехват после перестройки файрвола.
func TestInstall6_PersistError(t *testing.T) {
	fe := &v6Exec{persistErr: errors.New("no space left on device")}
	it := newV6IPTables(fe)
	if err := it.Install6(context.Background(), testV6Spec()); err == nil {
		t.Fatal("ошибка записи файлов правил обязана уйти наверх")
	}
	if fe.hookWrites != 0 {
		t.Errorf("хук без файлов правил писать незачем: hookWrites=%d", fe.hookWrites)
	}
}

func TestUninstall6(t *testing.T) {
	fe := &v6Exec{dump: v6JumpsDump()}
	it := newV6IPTables(fe)
	it.Uninstall6(context.Background())

	if fe.cleanups != 1 {
		t.Errorf("файлы правил и хук обязаны быть удалены, cleanups=%d", fe.cleanups)
	}
	for _, want := range [][]string{
		{"-t", "mangle", "-F", Chain6Name}, {"-t", "mangle", "-X", Chain6Name},
		{"-t", "nat", "-F", Redirect6Chain}, {"-t", "nat", "-X", Redirect6Chain},
	} {
		if !hasCall(fe.ip6t, want...) {
			t.Errorf("нет вызова ip6tables %v: %v", want, fe.ip6t)
		}
	}
	if !hasCall(fe.ip, "-6", "rule", "del", "fwmark", "0x1", "table", "100") {
		t.Errorf("нет ip -6 rule del: %v", fe.ip)
	}
	if !hasCall(fe.ip, "-6", "route", "del", "local", "::/0", "dev", "lo", "table", "100") {
		t.Errorf("нет ip -6 route del: %v", fe.ip)
	}
}

func TestProbe6(t *testing.T) {
	fe := &v6Exec{dump: v6JumpsDump()}
	it := newV6IPTables(fe)
	st, err := it.probe6(context.Background())
	if err != nil || !st.anyChain || !st.jumps {
		t.Errorf("полный слой: %+v, %v", st, err)
	}

	// Цепочки целы, джампы снесены перестройкой файрвола.
	fe.dump = "-N " + Chain6Name + "\n-N " + Redirect6Chain + "\n"
	st, _ = it.probe6(context.Background())
	if !st.anyChain || st.jumps {
		t.Errorf("без джампов: %+v, want anyChain без jumps", st)
	}

	fe.dump = "-P PREROUTING ACCEPT\n"
	st, _ = it.probe6(context.Background())
	if st.anyChain || st.jumps {
		t.Errorf("пустой netfilter: %+v, want нули", st)
	}

	fe.dumpErr = errors.New("xtables lock")
	if _, err := it.probe6(context.Background()); err == nil {
		t.Error("ошибка запроса обязана уйти наверх — это «неизвестно», а не «отсутствует»")
	}
}

// Боевой конструктор обязан отдавать IPTables с подключённым v6-слоем:
// выключается слой настройкой, а не отсутствием зависимостей.
func TestNewIPTables_HasV6Backend(t *testing.T) {
	if _, ok := NewIPTables().v6.(systemV6); !ok {
		t.Fatalf("NewIPTables обязан подключать systemV6, got %T", NewIPTables().v6)
	}
}

// ipv6_only: v4-цепочки остаются, но пустыми. Сверка режима tproxy наблюдает
// именно их, поэтому объявление цепочки и джамп обязаны сохраниться, а
// перехватывающих правил быть не должно. DNS-RESCUE остаётся — он про
// устройства вне политики.
func TestBuildRestoreInput_V4Off(t *testing.T) {
	spec := RestoreInputSpec{
		PolicyMark:     "0xffffaab",
		WANIPs:         []string{"203.0.113.7/32"},
		BypassCIDRs:    []string{"10.9.9.0/24"},
		BypassUDPPorts: []PortRange{{From: 123, To: 123}},
		LANBridges:     []LANBridgeDNSRedir{{Bridge: "br0", Port: 41100}},
		V4Off:          true,
	}
	got := buildRestoreInput(spec)

	for _, want := range []string{
		":" + ChainName + " - [0:0]",
		":" + RedirectChain + " - [0:0]",
		"-A PREROUTING -m connmark --mark 0xffffaab -m conntrack ! --ctstate INVALID -j " + ChainName,
		"-A PREROUTING -m connmark --mark 0xffffaab -m conntrack ! --ctstate INVALID -j " + RedirectChain,
		"--comment \"" + DNSRescueTag + "\" -j REDIRECT --to-ports 41100",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("нет %q:\n%s", want, got)
		}
	}
	for _, banned := range []string{"-j TPROXY", "-A " + RedirectChain + " ", "-A " + ChainName + " "} {
		if strings.Contains(got, banned) {
			t.Errorf("в пустых цепочках не должно быть %q:\n%s", banned, got)
		}
	}
	// Живая сверка обязана видеть такой набор как «установлено и входится».
	dump := "-N " + ChainName + "\n-N " + RedirectChain + "\n" + got
	if chain, jump := scanChain(dump, ChainName); !chain || !jump {
		t.Errorf("mangle: chain=%v jump=%v, want true, true", chain, jump)
	}
	if chain, jump := scanChain(dump, RedirectChain); !chain || !jump {
		t.Errorf("nat: chain=%v jump=%v, want true, true", chain, jump)
	}

	on := spec
	on.V4Off = false
	if equalInstalledSpec(&spec, &on) {
		t.Error("смена V4Off обязана менять рендер — иначе переключение семейства не переустановит правила")
	}
}

// Вынос DNS-RESCUE в emitDNSRescue не должен был изменить обычный рендер.
func TestBuildNatRestoreInput_DNSRescueUnchanged(t *testing.T) {
	spec := RestoreInputSpec{
		PolicyMark: "0xffffaab",
		LANBridges: []LANBridgeDNSRedir{{Bridge: "br0", Port: 41100}},
	}
	got := buildNatRestoreInput(spec)
	udp := `-I PREROUTING 1 -i br0 -m mark --mark 0x0 -m pkttype --pkt-type unicast -p udp --dport 53 -m comment --comment "AWGM-DNS-RESCUE" -j REDIRECT --to-ports 41100`
	tcp := strings.Replace(udp, "-p udp", "-p tcp", 1)
	if !strings.Contains(got, udp+"\n"+tcp+"\nCOMMIT\n") {
		t.Errorf("DNS-RESCUE обязан стоять последним перед COMMIT, udp затем tcp:\n%s", got)
	}
	spec.MatchAll = true
	if strings.Contains(buildNatRestoreInput(spec), DNSRescueTag) {
		t.Error("в режиме «все устройства» DNS-RESCUE не эмитится")
	}
}

// F677 в v6: тот же DROP в ip6tables mangle INPUT, снос по тегу на Install6 и
// Uninstall6 (restore идёт с --noflush) и в хуке перед восстановлением mangle.
func TestSelfLoopDrop6(t *testing.T) {
	mangle := buildMangleRestoreInput6(testV6Spec())
	for _, line := range selfLoopDropLines() {
		if !strings.Contains(mangle, line) {
			t.Errorf("нет %q:\n%s", line, mangle)
		}
	}

	line := `-A INPUT -p tcp --dport 51273 -m conntrack ! --ctstate DNAT -m comment --comment "` + SelfLoopTag + `" -j DROP`
	del := append([]string{"-t", "mangle"}, strings.Fields("-D"+strings.TrimPrefix(line, "-A"))...)
	for name, run := range map[string]func(*IPTables){
		"Install6":   func(it *IPTables) { _ = it.Install6(context.Background(), testV6Spec()) },
		"Uninstall6": func(it *IPTables) { it.Uninstall6(context.Background()) },
	} {
		fe := &v6Exec{dump: v6JumpsDump() + line + "\n"}
		run(newV6IPTables(fe))
		if !hasCall(fe.ip6t, del...) {
			t.Errorf("%s не снял self-loop DROP: %v", name, fe.ip6t)
		}
	}

	if !strings.Contains(netfilter6HookScript(), "scrub_tagged mangle "+SelfLoopTag+" INPUT") {
		t.Error("v6-хук не чистит self-loop DROP перед restore mangle")
	}
}
