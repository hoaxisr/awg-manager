package singbox

import (
	"os"
	"path/filepath"
	"testing"
)

func TestUplinkProbeRouteCannotBypassTunnel(t *testing.T) {
	for _, tc := range []struct {
		name, before, own, after string
		ok                       bool
	}{
		{"dedicated", "", `{"inbound":"a-in","outbound":"a"}`, "", true},
		{"generated-sniff", `{"inbound":["a-in"],"action":"sniff","timeout":"300ms"}`, `{"inbound":"a-in","outbound":"a"}`, "", true},
		{"dns-hijack", `{"inbound":["a-in"],"action":"hijack-dns","port":[53]}`, `{"inbound":"a-in","outbound":"a"}`, "", true},
		{"udp-options", `{"inbound":["a-in"],"network":"udp","action":"route-options","udp_timeout":"15s"}`, `{"inbound":"a-in","outbound":"a"}`, "", true},
		{"inverted-udp-direct", `{"network":"udp","invert":true,"outbound":"direct"}`, `{"inbound":"a-in","outbound":"a"}`, "", false},
		{"443-direct", `{"inbound":"a-in","port":[443],"outbound":"direct"}`, `{"inbound":"a-in","outbound":"a"}`, "", false},
		{"range-direct", `{"inbound":"a-in","port":[53],"port_range":["400:500"],"outbound":"direct"}`, `{"inbound":"a-in","outbound":"a"}`, "", false},
		{"malformed-port-direct", `{"inbound":"a-in","port":["443"],"outbound":"direct"}`, `{"inbound":"a-in","outbound":"a"}`, "", false},
		{"earlier-direct", `{"outbound":"direct"}`, `{"inbound":"a-in","outbound":"a"}`, "", false},
		{"earlier-group", `{"inbound":"a-in","outbound":"some-group"}`, `{"inbound":"a-in","outbound":"a"}`, "", false},
		{"conditional", "", `{"inbound":"a-in","domain":"example.com","outbound":"a"}`, "", false},
		{"other-inbound", `{"inbound":"b-in","outbound":"b"}`, `{"inbound":"a-in","outbound":"a"}`, "", true},
		{"later-direct", "", `{"inbound":"a-in","outbound":"a"}`, `{"outbound":"direct"}`, true},
		{"missing-route", "", "", "", false},
		{"list-inbound", "", `{"inbound":["a-in"],"outbound":"a","action":"route"}`, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			write := func(name, text string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if tc.before != "" {
				write("00-before.json", `{"route":{"rules":[`+tc.before+`]}}`)
			}
			rules := ""
			if tc.own != "" {
				rules = tc.own
			}
			write("10-tunnels.json", `{"inbounds":[{"tag":"a-in","type":"mixed","listen":"127.0.0.1","listen_port":1088}],"route":{"rules":[`+rules+`]}}`)
			if tc.after != "" {
				write("20-after.json", `{"route":{"rules":[`+tc.after+`]}}`)
			}
			port, err := verifyUplinkRoute(dir, "a")
			if tc.ok && (err != nil || port != 1088) {
				t.Fatal(port, err)
			}
			if !tc.ok && err == nil {
				t.Fatal("unsafe route accepted")
			}
		})
	}
}

func TestUplinkProbeRejectsDuplicateInbound(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"10-tunnels.json", "90-user.json"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(`{"inbounds":[{"tag":"a-in","type":"mixed","listen":"127.0.0.1","listen_port":1088}],"route":{"rules":[{"inbound":"a-in","outbound":"a"}]}}`), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := verifyUplinkRoute(dir, "a"); err == nil {
		t.Fatal("duplicate inbound accepted")
	}
}
