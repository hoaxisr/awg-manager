package router

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// ingressRecorder — сиды IPTables с записью вызовов, чтобы проверять НЕ только
// результат, но и отсутствие лишних мутаций в установившемся состоянии.
type ingressRecorder struct {
	natDump   string
	ruleDump  string
	routeDump string
	ipt       [][]string
	ip        [][]string
	ipErr     error
}

func (r *ingressRecorder) tables() *IPTables {
	return &IPTables{
		runIPTables: func(_ context.Context, args ...string) error {
			r.ipt = append(r.ipt, args)
			return nil
		},
		runIPTablesOut: func(_ context.Context, _ ...string) (string, error) { return r.natDump, nil },
		runIP: func(_ context.Context, args ...string) error {
			r.ip = append(r.ip, args)
			return r.ipErr
		},
		runIPOut: func(_ context.Context, args ...string) (string, error) {
			if len(args) > 0 && args[0] == "route" {
				return r.routeDump, nil
			}
			return r.ruleDump, nil
		},
	}
}

func (r *ingressRecorder) ipCalls(prefix ...string) int {
	n := 0
	for _, call := range r.ip {
		if len(call) < len(prefix) {
			continue
		}
		match := true
		for i, want := range prefix {
			if call[i] != want {
				match = false
				break
			}
		}
		if match {
			n++
		}
	}
	return n
}

const testTunDNS = "172.18.0.2"

func testIngressSpec(ifaces ...string) FakeIPIngressSpec {
	return FakeIPIngressSpec{TunIface: "opkgtun0", TunDNS: testTunDNS, Ifaces: ifaces}
}

// natDumpFor рендерит дамп `iptables -t nat -S PREROUTING` с нашими правилами
// впереди и чужим правилом NDMS после них — форма установившегося состояния.
func natDumpFor(ifaces ...string) string {
	var b strings.Builder
	b.WriteString("-P PREROUTING ACCEPT\n")
	for _, iface := range ifaces {
		for _, proto := range []string{"udp", "tcp"} {
			b.WriteString("-A PREROUTING -i " + iface + " -p " + proto + " -m " + proto +
				" --dport 53 -m comment --comment \"" + FakeIPIngressTag +
				"\" -j DNAT --to-destination " + testTunDNS + ":53\n")
		}
	}
	b.WriteString("-A PREROUTING -i br0 -p udp -m udp --dport 53 -j REDIRECT --to-ports 40500\n")
	return b.String()
}

// routeDumpFor рендерит дамп `ip route show table <ours>` в установившемся
// состоянии: default в tun + throw-исключения приватных сетей.
func routeDumpFor(tunIface string) string {
	if tunIface == "" {
		return ""
	}
	var b strings.Builder
	b.WriteString("default dev " + tunIface + " scope link\n")
	for _, cidr := range fakeIPIngressThrowCIDRs {
		b.WriteString("throw " + cidr + "\n")
	}
	return b.String()
}

func ruleDumpFor(ifaces ...string) string {
	var b strings.Builder
	b.WriteString("0:\tfrom all lookup local\n")
	for _, iface := range ifaces {
		b.WriteString("29000:\tfrom all iif " + iface + " lookup " + fakeIPIngressTableStr() + "\n")
	}
	b.WriteString("32766:\tfrom all lookup main\n")
	return b.String()
}

func TestFakeIPIngressNATDrift(t *testing.T) {
	spec := testIngressSpec("opkgtun17")

	tests := []struct {
		name string
		dump string
		want bool
	}{
		{"установившееся состояние", natDumpFor("opkgtun17"), false},
		{"правил нет вовсе", "-P PREROUTING ACCEPT\n", true},
		{
			name: "потерян tcp",
			dump: "-P PREROUTING ACCEPT\n-A PREROUTING -i opkgtun17 -p udp -m udp --dport 53 " +
				"-m comment --comment \"" + FakeIPIngressTag + "\" -j DNAT --to-destination " + testTunDNS + ":53\n",
			want: true,
		},
		{
			name: "чужое правило встало выше нашего",
			dump: "-P PREROUTING ACCEPT\n-A PREROUTING -i br0 -p udp -m udp --dport 53 -j REDIRECT --to-ports 40500\n" +
				strings.TrimPrefix(natDumpFor("opkgtun17"), "-P PREROUTING ACCEPT\n"),
			want: true,
		},
		{
			name: "DNAT бьёт на чужой адрес",
			dump: strings.ReplaceAll(natDumpFor("opkgtun17"), testTunDNS+":53", "10.0.0.1:53"),
			want: true,
		},
		{
			name: "остался интерфейс, снятый с заворота",
			dump: natDumpFor("opkgtun17", "opkgtun18"),
			want: true,
		},
		{
			name: "имя-префикс не считается совпадением",
			dump: natDumpFor("opkgtun1"),
			want: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := fakeIPIngressNATDrift(tt.dump, spec); got != tt.want {
				t.Fatalf("fakeIPIngressNATDrift() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFakeIPIngressRuleDrift(t *testing.T) {
	spec := testIngressSpec("opkgtun17")

	if fakeIPIngressRuleDrift(ruleDumpFor("opkgtun17"), spec) {
		t.Error("установившееся состояние прочитано как дрейф")
	}
	if !fakeIPIngressRuleDrift(ruleDumpFor(), spec) {
		t.Error("пропавшее iif-правило не распознано как дрейф")
	}
	if !fakeIPIngressRuleDrift(ruleDumpFor("opkgtun17", "opkgtun18"), spec) {
		t.Error("лишнее правило в нашей таблице не распознано как дрейф")
	}
}

func TestEnsureFakeIPIngress_SteadyStateDoesNotMutate(t *testing.T) {
	rec := &ingressRecorder{
		natDump:   natDumpFor("opkgtun17"),
		ruleDump:  ruleDumpFor("opkgtun17"),
		routeDump: routeDumpFor("opkgtun0"),
	}
	if err := rec.tables().EnsureFakeIPIngress(context.Background(), testIngressSpec("opkgtun17")); err != nil {
		t.Fatalf("EnsureFakeIPIngress() error = %v", err)
	}
	if len(rec.ipt) != 0 || len(rec.ip) != 0 {
		t.Errorf("в установившемся состоянии есть мутации: iptables=%v ip=%v", rec.ipt, rec.ip)
	}
}

func TestEnsureFakeIPIngress_RebuildsOnTunIndexChange(t *testing.T) {
	// fakeip-tun переехал на другой индекс: default в таблице указывает на
	// прежний интерфейс — правила и маршруты должны пересобраться.
	rec := &ingressRecorder{
		natDump:   natDumpFor("opkgtun17"),
		ruleDump:  ruleDumpFor("opkgtun17"),
		routeDump: routeDumpFor("opkgtun1"),
	}
	if err := rec.tables().EnsureFakeIPIngress(context.Background(), testIngressSpec("opkgtun17")); err != nil {
		t.Fatalf("EnsureFakeIPIngress() error = %v", err)
	}
	if n := rec.ipCalls("route", "add", "default", "dev", "opkgtun0"); n != 1 {
		t.Errorf("default не переставлен на новый tun: %v", rec.ip)
	}
	if n := rec.ipCalls("route", "flush", "table", fakeIPIngressTableStr()); n != 1 {
		t.Errorf("таблица не очищена перед пересборкой: %v", rec.ip)
	}
}

func TestFakeIPIngressRouteDrift(t *testing.T) {
	spec := testIngressSpec("opkgtun17")

	if fakeIPIngressRouteDrift(routeDumpFor("opkgtun0"), spec) {
		t.Error("установившееся состояние прочитано как дрейф")
	}
	if !fakeIPIngressRouteDrift("", spec) {
		t.Error("пустая таблица не распознана как дрейф")
	}
	if !fakeIPIngressRouteDrift("default dev opkgtun0 scope link\n", spec) {
		t.Error("отсутствие throw-исключений не распознано как дрейф")
	}
	if !fakeIPIngressRouteDrift(routeDumpFor("opkgtun0")+"192.168.5.0/24 dev br0\n", spec) {
		t.Error("лишний маршрут в нашей таблице не распознан как дрейф")
	}
}

func TestEnsureFakeIPIngress_InstallsOnDrift(t *testing.T) {
	rec := &ingressRecorder{natDump: "-P PREROUTING ACCEPT\n", ruleDump: ruleDumpFor()}
	if err := rec.tables().EnsureFakeIPIngress(context.Background(), testIngressSpec("opkgtun17")); err != nil {
		t.Fatalf("EnsureFakeIPIngress() error = %v", err)
	}

	var protos []string
	for _, call := range rec.ipt {
		joined := strings.Join(call, " ")
		if !strings.Contains(joined, "-I PREROUTING 1") || !strings.Contains(joined, FakeIPIngressTag) {
			t.Fatalf("неожиданный вызов iptables: %v", call)
		}
		if !strings.Contains(joined, "--to-destination "+testTunDNS+":53") {
			t.Errorf("DNAT не на DNS туннеля: %v", call)
		}
		if !strings.Contains(joined, "-i opkgtun17") {
			t.Errorf("DNAT не на ingress-интерфейсе: %v", call)
		}
		for _, p := range []string{"udp", "tcp"} {
			if strings.Contains(joined, "-p "+p) {
				protos = append(protos, p)
			}
		}
	}
	if len(protos) != 2 {
		t.Fatalf("ожидались правила для udp и tcp, получено %v (вызовы: %v)", protos, rec.ipt)
	}
	if n := rec.ipCalls("rule", "add", "iif", "opkgtun17", "table", fakeIPIngressTableStr()); n != 1 {
		t.Errorf("iif-правило не добавлено: %v", rec.ip)
	}
	if n := rec.ipCalls("route", "add", "default", "dev", "opkgtun0", "table", fakeIPIngressTableStr()); n != 1 {
		t.Errorf("default-маршрут в tun не добавлен: %v", rec.ip)
	}
	for _, cidr := range fakeIPIngressThrowCIDRs {
		if n := rec.ipCalls("route", "add", "throw", cidr, "table", fakeIPIngressTableStr()); n != 1 {
			t.Errorf("throw-исключение %s не добавлено (LAN клиентов ушёл бы в tun): %v", cidr, rec.ip)
		}
	}
}

func TestEnsureFakeIPIngress_RebuildScrubsStaleRules(t *testing.T) {
	// Сервер сняли с заворота: в ядре остались правила для двух интерфейсов,
	// желаемый состав — один.
	rec := &ingressRecorder{
		natDump:   natDumpFor("opkgtun17", "opkgtun18"),
		ruleDump:  ruleDumpFor("opkgtun17", "opkgtun18"),
		routeDump: routeDumpFor("opkgtun0"),
	}
	if err := rec.tables().EnsureFakeIPIngress(context.Background(), testIngressSpec("opkgtun17")); err != nil {
		t.Fatalf("EnsureFakeIPIngress() error = %v", err)
	}

	deleted, inserted := 0, 0
	for _, call := range rec.ipt {
		switch {
		case hasArg(call, "-D"):
			deleted++
		case hasArg(call, "-I"):
			inserted++
			if !hasArg(call, "opkgtun17") {
				t.Errorf("переустановлено правило снятого интерфейса: %v", call)
			}
		}
	}
	if deleted != 4 {
		t.Errorf("снято %d старых правил, ожидалось 4: %v", deleted, rec.ipt)
	}
	if inserted != 2 {
		t.Errorf("вставлено %d новых правил, ожидалось 2: %v", inserted, rec.ipt)
	}
	if n := rec.ipCalls("rule", "del", "table", fakeIPIngressTableStr()); n == 0 {
		t.Errorf("старые iif-правила не слиты: %v", rec.ip)
	}
}

func TestEnsureFakeIPIngress_EmptySpecRemovesAndIsQuietWhenClean(t *testing.T) {
	// Заворот был — снимаем.
	rec := &ingressRecorder{
		natDump:   natDumpFor("opkgtun17"),
		ruleDump:  ruleDumpFor("opkgtun17"),
		routeDump: routeDumpFor("opkgtun0"),
	}
	if err := rec.tables().EnsureFakeIPIngress(context.Background(), FakeIPIngressSpec{}); err != nil {
		t.Fatalf("EnsureFakeIPIngress() error = %v", err)
	}
	if len(rec.ipt) != 2 {
		t.Errorf("ожидалось снятие двух DNAT-правил, получено: %v", rec.ipt)
	}
	if n := rec.ipCalls("route", "flush", "table", fakeIPIngressTableStr()); n != 1 {
		t.Errorf("таблица не очищена: %v", rec.ip)
	}

	// Заворота не было — ни одной мутации (этот путь бежит каждый тик reap'а
	// в tproxy-режиме).
	clean := &ingressRecorder{natDump: "-P PREROUTING ACCEPT\n", ruleDump: ruleDumpFor()}
	if err := clean.tables().EnsureFakeIPIngress(context.Background(), FakeIPIngressSpec{}); err != nil {
		t.Fatalf("EnsureFakeIPIngress() error = %v", err)
	}
	if len(clean.ipt) != 0 || len(clean.ip) != 0 {
		t.Errorf("чистая система тронута: iptables=%v ip=%v", clean.ipt, clean.ip)
	}
}

func TestEnsureFakeIPIngress_ExistingIPRuleIsNotAnError(t *testing.T) {
	rec := &ingressRecorder{
		natDump:  "-P PREROUTING ACCEPT\n",
		ruleDump: ruleDumpFor(),
		ipErr:    errors.New("exit status 2 (exit 2, stderr: RTNETLINK answers: File exists)"),
	}
	if err := rec.tables().EnsureFakeIPIngress(context.Background(), testIngressSpec("opkgtun17")); err == nil {
		t.Fatal("ожидалась ошибка на ip route add")
	} else if !strings.Contains(err.Error(), "ip route add") {
		t.Fatalf("неожиданная ошибка: %v", err)
	}

	// «File exists» именно на добавлении iif-правила — идемпотентный случай.
	rec2 := &ingressRecorder{natDump: "-P PREROUTING ACCEPT\n", ruleDump: ruleDumpFor()}
	it := rec2.tables()
	it.runIP = func(_ context.Context, args ...string) error {
		rec2.ip = append(rec2.ip, args)
		if args[0] == "rule" && args[1] == "add" {
			return errors.New("exit status 2 (exit 2, stderr: RTNETLINK answers: File exists)")
		}
		return nil
	}
	if err := it.EnsureFakeIPIngress(context.Background(), testIngressSpec("opkgtun17")); err != nil {
		t.Fatalf("существующее iif-правило прочитано как ошибка: %v", err)
	}
}

func hasArg(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

// Имя таблицы вместо номера (запись в /etc/iproute2/rt_tables) не должно
// читаться как дрейф — иначе правила пересобирались бы каждый тик.
func TestFakeIPIngressRuleDrift_NamedTable(t *testing.T) {
	dump := "0:\tfrom all lookup local\n" +
		"29000:\tfrom all iif opkgtun17 lookup awgm-fakeip\n" +
		"32766:\tfrom all lookup main\n"
	if fakeIPIngressRuleDrift(dump, testIngressSpec("opkgtun17")) {
		t.Error("правило с именованной таблицей прочитано как дрейф")
	}
}
