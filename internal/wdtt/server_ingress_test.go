package wdtt

import "testing"

func TestWdttServerIngressRefs(t *testing.T) {
	got := WdttServerIngressRefs("opkgtun17", "")
	want := []string{"iface:opkgtun17", "iface:wdttraw0"}
	if len(got) != len(want) {
		t.Fatalf("refs = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("refs[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	got = WdttServerIngressRefs("", "")
	if got[0] != "iface:wdtt0" {
		t.Fatalf("default wg ref = %q", got[0])
	}
}

func TestEnsureWdttIngressRefs(t *testing.T) {
	refs := []string{"iface:opkgtun17", "managed:Wireguard3"}
	next, changed := EnsureWdttIngressRefs(refs, "opkgtun17", "")
	if !changed {
		t.Fatal("expected change when raw ref missing")
	}
	if !containsString(next, "iface:wdttraw0") {
		t.Fatalf("next = %v", next)
	}
	if !containsString(next, "managed:Wireguard3") {
		t.Fatalf("must preserve unrelated refs: %v", next)
	}

	next, changed = EnsureWdttIngressRefs(next, "opkgtun17", "")
	if changed {
		t.Fatalf("already paired, changed again: %v", next)
	}
}

func TestRemoveWdttIngressRefs(t *testing.T) {
	refs := []string{"iface:opkgtun17", "iface:wdttraw0", "iface:nwg3"}
	next, changed := RemoveWdttIngressRefs(refs, "opkgtun17", "")
	if !changed {
		t.Fatal("expected change")
	}
	if containsString(next, "iface:opkgtun17") || containsString(next, "iface:wdttraw0") {
		t.Fatalf("next = %v", next)
	}
	if !containsString(next, "iface:nwg3") {
		t.Fatalf("unrelated ref removed: %v", next)
	}
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// Переезд raw-половины в OpkgTun обязан не только добавить новую ссылку, но и
// снять протухшую: iface:wdttraw0 указывал бы на интерфейс, которого больше
// нет, и ingress sing-box слушал бы пустоту.
func TestEnsureWdttIngressRefsDropsLegacyRawRef(t *testing.T) {
	refs := []string{"iface:opkgtun17", "iface:wdttraw0", "managed:Wireguard3"}
	next, changed := EnsureWdttIngressRefs(refs, "opkgtun17", "opkgtun18")
	if !changed {
		t.Fatalf("переезд не отражён: %v", next)
	}
	if containsString(next, "iface:wdttraw0") {
		t.Fatalf("протухшая ссылка осталась: %v", next)
	}
	if !containsString(next, "iface:opkgtun18") {
		t.Fatalf("новая raw-ссылка не добавлена: %v", next)
	}
	if !containsString(next, "managed:Wireguard3") {
		t.Fatalf("чужая ссылка потеряна: %v", next)
	}

	next, changed = EnsureWdttIngressRefs(next, "opkgtun17", "opkgtun18")
	if changed {
		t.Fatalf("повторный проход снова что-то поменял: %v", next)
	}
}
