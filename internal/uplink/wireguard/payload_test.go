package wireguard

import "testing"

func TestPayloadPrefixesDoNotExpandPeerScope(t *testing.T) {
	for _, v := range [][]string{nil, {"0.0.0.0/0"}, {"0.0.0.0/0", "::/0"}} {
		if !validPayloadPrefixes(v) {
			t.Fatal(v)
		}
	}
	for _, v := range [][]string{{"::/0"}, {"10.0.0.0/8"}, {"0.0.0.0/0", "10.0.0.0/8"}, {"0.0.0.0/0", "::/0", "::/0"}} {
		if validPayloadPrefixes(v) {
			t.Fatal("unsupported peer scope accepted", v)
		}
	}
}
