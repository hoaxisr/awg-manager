package singbox

import "testing"

// proxyIsOurs decides whether an NDMS ProxyN belongs to awg-manager's sing-box
// management, so disable/orphan-cleanup removes it. Subscription composites are
// the regression case: their proxy carries the subscription *label* as the
// interface description (not a tunnel tag), so the tag/slot heuristics miss it —
// they must be recognised via their explicitly-tracked proxy index.
func TestProxyIsOurs(t *testing.T) {
	tunnelTags := map[string]bool{"vless-1": true}
	ourPortSlots := map[int]bool{3: true}
	subProxyIdx := map[int]bool{7: true}

	cases := []struct {
		name string
		idx  int
		desc string
		want bool
	}{
		{"tunnel matched by description tag", 0, "vless-1", true},
		{"tunnel matched by port slot (empty desc)", 3, "", true},
		{"subscription composite (label description)", 7, "Моя подписка", true},
		{"foreign proxy with description", 9, "some-other-app", false},
		{"foreign proxy empty desc unknown slot", 5, "", false},
	}
	for _, c := range cases {
		got := proxyIsOurs(c.idx, c.desc, tunnelTags, ourPortSlots, subProxyIdx)
		if got != c.want {
			t.Errorf("%s: proxyIsOurs(%d, %q) = %v, want %v", c.name, c.idx, c.desc, got, c.want)
		}
	}
}

// nativeProxyKernelNames must return kernel names of ONLY the proxies that are
// not ours — the KeenOS-native SOCKS proxies a user can bind a router outbound
// to (#323). Ours (by tunnel tag or by port slot) are excluded.
func TestNativeProxyKernelNames(t *testing.T) {
	proxies := []proxyEntry{
		{idx: 0, desc: "My-Socks5", kernel: "t2s0"},      // native — keep
		{idx: 1, desc: "vless-1", kernel: "t2s1"},        // ours by tunnel tag — drop
		{idx: 2, desc: "", kernel: "t2s2"},               // ours by port slot — drop
		{idx: 3, desc: "another-native", kernel: "t2s3"}, // native — keep
	}
	got := nativeProxyKernelNames(proxies,
		map[string]bool{"vless-1": true}, // tunnelTags
		map[int]bool{2: true},            // ourPortSlots
		map[int]bool{},                   // subProxyIdx
	)
	if len(got) != 2 {
		t.Fatalf("want 2 native, got %d: %v", len(got), got)
	}
	want := map[string]bool{"t2s0": true, "t2s3": true}
	for _, k := range got {
		if !want[k] {
			t.Errorf("unexpected native proxy %q in %v", k, got)
		}
	}
}
