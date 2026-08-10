package wdtt

import "testing"

func TestWdttServerIngressRefs(t *testing.T) {
	got := WdttServerIngressRefs("opkgtun17")
	want := []string{"iface:opkgtun17", "iface:wdttraw0"}
	if len(got) != len(want) {
		t.Fatalf("refs = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("refs[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	got = WdttServerIngressRefs("")
	if got[0] != "iface:wdtt0" {
		t.Fatalf("default wg ref = %q", got[0])
	}
}

func TestEnsureWdttIngressRefs(t *testing.T) {
	refs := []string{"iface:opkgtun17", "managed:Wireguard3"}
	next, changed := EnsureWdttIngressRefs(refs, "opkgtun17")
	if !changed {
		t.Fatal("expected change when raw ref missing")
	}
	if !containsString(next, "iface:wdttraw0") {
		t.Fatalf("next = %v", next)
	}
	if !containsString(next, "managed:Wireguard3") {
		t.Fatalf("must preserve unrelated refs: %v", next)
	}

	next, changed = EnsureWdttIngressRefs(next, "opkgtun17")
	if changed {
		t.Fatalf("already paired, changed again: %v", next)
	}
}

func TestRemoveWdttIngressRefs(t *testing.T) {
	refs := []string{"iface:opkgtun17", "iface:wdttraw0", "iface:nwg3"}
	next, changed := RemoveWdttIngressRefs(refs, "opkgtun17")
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
