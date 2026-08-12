//go:build linux

package listenfirewall

import (
	"strings"
	"testing"
)

// Правила без метки — откат Apply и наследие версий до неё; отличить их от
// чужих можно только по форме, ровно той, что печатает Apply, без единого
// лишнего токена. Правила с уточнениями (-i, -s, -m state) ставит не AWG
// Manager: их держат в INPUT и NDM, и другие пакеты, а сверка, приняв их за
// свои, каждый тик тратила по 6 вызовов iptables на каждое и могла снести
// одноимённое правило без уточнений.
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

// Метка делает владение правилом однозначным: сверка не спутает его с чужим
// разрешением на тот же порт. Голая форма остаётся только как откат для
// прошивки без xt_comment и для правил, поставленных версиями до метки.
func TestListenRuleArgs(t *testing.T) {
	tagged := listenRuleArgs(56002, "udp", true)
	want := []string{"-p", "udp", "-m", "udp", "--dport", "56002",
		"-m", "comment", "--comment", Comment, "-j", "ACCEPT"}
	if !equalArgs(tagged, want) {
		t.Fatalf("с меткой: %v, ожидали %v", tagged, want)
	}

	bare := listenRuleArgs(56002, "udp", false)
	wantBare := []string{"-p", "udp", "-m", "udp", "--dport", "56002", "-j", "ACCEPT"}
	if !equalArgs(bare, wantBare) {
		t.Fatalf("без метки: %v, ожидали %v", bare, wantBare)
	}
	// Голая форма обязана совпадать с той, что признаёт bareListenRule:
	// разъехавшись, Apply ставил бы правило, которого сверка не узнаёт.
	fields := append([]string{"-A", "INPUT"}, bare...)
	if spec, ok := bareListenRule(fields); !ok || spec.Port != 56002 || spec.Proto != "udp" {
		t.Fatalf("bareListenRule не узнал собственную форму Apply: %v ok=%v", spec, ok)
	}
}

func equalArgs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Хук восстанавливает правила после того, как NDMS перезаписал таблицы. Он
// обязан ставить ту же помеченную форму, иначе после первого же flap правила
// снова становятся голыми и владение опять неотличимо.
func TestListenNetfilterHookScriptTagsRules(t *testing.T) {
	script := listenNetfilterHookScript([]PortSpec{{Port: 56002, Proto: "udp"}})
	if !strings.Contains(script, "-m comment --comment "+Comment) {
		t.Fatalf("хук ставит правила без метки:\n%s", script)
	}
	if !strings.Contains(script, "-I INPUT 1 -p udp -m udp --dport 56002 -j ACCEPT") {
		t.Fatalf("в хуке нет отката на голую форму:\n%s", script)
	}
}
