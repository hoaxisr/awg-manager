//go:build linux

package listenfirewall

import "testing"

// Наши правила пишутся БЕЗ `-m comment`: на Keenetic xt_comment часто не
// загружен (#666). Отличить их от чужих можно только по форме — ровно той, что
// печатает Apply, без единого лишнего токена. Правила с уточнениями (-i, -s,
// -m state) ставит не AWG Manager: их держат в INPUT и NDM, и другие пакеты, а
// сверка, приняв их за свои, каждый тик тратила по 6 вызовов iptables на
// каждое и могла снести одноимённое правило без уточнений.
func TestParseManagedClaimsOnlyOurRules(t *testing.T) {
	out := `-P INPUT ACCEPT
-A INPUT -p udp -m udp --dport 56002 -j ACCEPT
-A INPUT -p tcp -m tcp --dport 8080 -j ACCEPT
-A INPUT -i br0 -p udp -m udp --dport 500 -j ACCEPT
-A INPUT -s 10.0.0.0/8 -p udp -m udp --dport 53 -j ACCEPT
-A INPUT -p tcp -m tcp --dport 22 -m state --state NEW -j ACCEPT
-A INPUT -p udp -m udp --dport 4500:4510 -j ACCEPT
-A FORWARD -p udp -m udp --dport 56002 -j ACCEPT
-A INPUT -p udp -m udp --dport 51820 -m comment --comment ` + Comment + ` -j ACCEPT
-A INPUT -p udp -m udp --dport 56002 -j ACCEPT`

	got := parseManaged(out)
	want := []PortSpec{
		{Port: 56002, Proto: "udp"},
		{Port: 8080, Proto: "tcp"},
		{Port: 51820, Proto: "udp"},
	}
	if len(got) != len(want) {
		t.Fatalf("parseManaged вернул %v, ожидали %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("parseManaged[%d] = %v, ожидали %v (всё: %v)", i, got[i], want[i], got)
		}
	}
}

func TestParseManagedEmpty(t *testing.T) {
	if got := parseManaged("-P INPUT ACCEPT\n"); len(got) != 0 {
		t.Fatalf("на чистом INPUT ожидали пусто, получили %v", got)
	}
}
