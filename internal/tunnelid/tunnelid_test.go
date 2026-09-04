package tunnelid

import "testing"

func TestValid(t *testing.T) {
	for _, id := range []string{"awg12", "nwg3", "a", "Tunnel_1-x", "a" + string(make([]byte, 0))} {
		if !Valid(id) {
			t.Errorf("Valid(%q) = false, want true", id)
		}
	}
	for _, id := range []string{"", "../settings", "1abc", "awg/1", "awg.json", "awg 1", "a\x00", string(make([]byte, 33))} {
		if Valid(id) {
			t.Errorf("Valid(%q) = true, want false", id)
		}
	}
	long := "a"
	for len(long) < 32 {
		long += "b"
	}
	if !Valid(long) {
		t.Errorf("32-char id rejected")
	}
	if Valid(long + "c") {
		t.Errorf("33-char id accepted")
	}
}
