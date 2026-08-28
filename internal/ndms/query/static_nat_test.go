package query

import (
	"encoding/json"
	"testing"
)

// Verbatim /show/rc/ip/static rows from a live router (2026-08-20): a port
// forward with an explicit to-port, and one created from the router UI with
// destination "this Keenetic" — loopback, and no to-port because it equals
// port.
const liveIPStatic = `[
  { "interface":"PPPoE0","protocol":"tcp","port":"18022","to-port":"2222",
    "to-address":"192.168.0.1","index":"79d2","comment":"" },
  { "interface":"PPPoE0","protocol":"tcp","port":"2222",
    "to-address":"127.0.0.1","index":"cea9","comment":"test" }
]`

func TestStaticNATEntry_DecodeLiveShape(t *testing.T) {
	var entries []StaticNATEntry
	if err := json.Unmarshal([]byte(liveIPStatic), &entries); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(entries))
	}
	if got := entries[0]; got.Protocol != "tcp" || got.Port != "18022" || got.ToPort != "2222" || got.ToAddress != "192.168.0.1" {
		t.Errorf("entry[0] = %#v", got)
	}
	if got := entries[1]; got.ToPort != "" || got.ToAddress != "127.0.0.1" {
		t.Errorf("entry[1] = %#v", got)
	}
}

func TestStaticNATEntry_TargetPort(t *testing.T) {
	cases := []struct {
		entry StaticNATEntry
		want  string
	}{
		{StaticNATEntry{Port: "18022", ToPort: "2222"}, "2222"},
		{StaticNATEntry{Port: "2222"}, "2222"}, // to-port omitted = same as port
		{StaticNATEntry{}, ""},                 // plain static NAT row
	}
	for _, c := range cases {
		if got := c.entry.TargetPort(); got != c.want {
			t.Errorf("TargetPort(%#v) = %q, want %q", c.entry, got, c.want)
		}
	}
}
