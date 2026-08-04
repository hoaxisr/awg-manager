package router

import "testing"

func TestUsesTunInbound(t *testing.T) {
	cases := map[string]bool{"fakeip-tun": true, "policy-tun": true, "tproxy": false, "": false}
	for mode, want := range cases {
		if got := usesTunInbound(mode); got != want {
			t.Errorf("usesTunInbound(%q)=%v, want %v", mode, got, want)
		}
	}
}
