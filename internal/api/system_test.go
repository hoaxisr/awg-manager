package api

import (
	"testing"
)

func TestEvalNativewg(t *testing.T) {
	cases := []struct {
		name          string
		hasComponent  bool
		supportsASC   bool
		awgProxy      bool
		wantAvailable bool
		wantReason    string
	}{
		{"no wireguard component", false, false, false, false, nwgReasonNoComponent},
		{"no component even with proxy", false, false, true, false, nwgReasonNoComponent},
		{"component + ASC firmware", true, true, false, true, ""},
		{"component + awg_proxy obfuscation", true, false, true, true, ""},
		{"component but no obfuscation path", true, false, false, false, nwgReasonNoObfuscation},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			avail, reason := evalNativewg(c.hasComponent, c.supportsASC, c.awgProxy)
			if avail != c.wantAvailable {
				t.Fatalf("available=%v want %v", avail, c.wantAvailable)
			}
			if reason != c.wantReason {
				t.Fatalf("reason=%q want %q", reason, c.wantReason)
			}
		})
	}
}
