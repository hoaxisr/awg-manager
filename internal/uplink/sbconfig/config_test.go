package sbconfig

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/uplink"
)

func fixture() map[string]any {
	return map[string]any{
		"outbounds": []any{
			map[string]any{"type": "selector", "tag": "sub", "outbounds": []any{"node"}},
			map[string]any{"type": "vless", "tag": "node", "server": "edge.example.com", "server_port": 443, "tls": map[string]any{"enabled": true, "server_name": "cover.example.com"}, "transport": map[string]any{"type": "ws", "headers": map[string]any{"Host": "http.example.com"}}},
		},
		"dns": map[string]any{"servers": []any{map[string]any{"type": "udp", "tag": "user-dns", "server": "9.9.9.9"}}},
	}
}
func binding() uplink.Binding {
	return uplink.Binding{Device: "wan0", IPv4: "192.0.2.2", DNSServers: []string{"192.0.2.53:5353"}}
}
func cloneDoc(t *testing.T, doc map[string]any) map[string]any {
	t.Helper()
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	var cp map[string]any
	json.Unmarshal(b, &cp)
	return cp
}
func node(doc map[string]any) map[string]any { return doc["outbounds"].([]any)[1].(map[string]any) }

func TestDomainBindingPreservesHostTLSAndUsesWANResolver(t *testing.T) {
	doc := fixture()
	before := cloneDoc(t, doc)
	out, err := Apply(doc, []Spec{{Tag: "sub", Binding: binding(), Group: true}})
	if err != nil {
		t.Fatal(err)
	}
	ob := node(out)
	for _, key := range []string{"server", "tls", "transport"} {
		if !sameJSON(ob[key], node(before)[key]) {
			t.Fatal("identity changed", key)
		}
	}
	if !sameJSON(doc, before) {
		t.Fatal("input was mutated")
	}
	if err = Verify(out, "sub", "wan0", true); err != nil {
		t.Fatal(err)
	}
	servers := out["dns"].(map[string]any)["servers"].([]any)
	if len(servers) != 2 || servers[0].(map[string]any)["tag"] != "user-dns" {
		t.Fatal(servers)
	}
	resolver := servers[1].(map[string]any)
	if resolver["bind_interface"] != "wan0" || resolver["inet4_bind_address"] != "192.0.2.2" || resolver["server"] != "192.0.2.53" || resolver["server_port"] != uint16(5353) {
		t.Fatal(resolver)
	}
}

func TestDNSAndAllMembersFollowProviderSwitchAndRefresh(t *testing.T) {
	out, err := Apply(fixture(), []Spec{{Tag: "sub", Binding: binding(), Group: true}})
	if err != nil {
		t.Fatal(err)
	}
	obs := out["outbounds"].([]any)
	obs[0].(map[string]any)["outbounds"] = []any{"node", "new-node"}
	out["outbounds"] = append(obs, map[string]any{"type": "trojan", "tag": "new-node", "server": "new.example.org", "server_port": 443})
	next := uplink.Binding{Device: "wan1", IPv4: "198.51.100.2", DNSServers: []string{"198.51.100.53"}}
	out, err = Apply(out, []Spec{{Tag: "sub", Binding: next, Group: true}})
	if err != nil {
		t.Fatal(err)
	}
	if err = Verify(out, "sub", "wan1", true); err != nil {
		t.Fatal(err)
	}
	if err = Verify(out, "sub", "wan0", true); err == nil {
		t.Fatal("old provider accepted")
	}
	for _, v := range out["dns"].(map[string]any)["servers"].([]any) {
		s := v.(map[string]any)
		if s["tag"] != "user-dns" && s["bind_interface"] != "wan1" {
			t.Fatal(s)
		}
	}
}

func TestNoSystemDNSOrDirectFallback(t *testing.T) {
	for _, kind := range []string{"no-dns", "domain-dns", "direct", "cycle", "detour", "mark"} {
		t.Run(kind, func(t *testing.T) {
			doc := fixture()
			b := binding()
			switch kind {
			case "no-dns":
				b.DNSServers = nil
			case "domain-dns":
				b.DNSServers = []string{"dns.example.com"}
			case "direct":
				node(doc)["type"] = "direct"
			case "cycle":
				doc["outbounds"].([]any)[0].(map[string]any)["outbounds"] = []any{"sub"}
			case "detour":
				node(doc)["detour"] = "other"
			case "mark":
				node(doc)["routing_mark"] = 123
			}
			before := cloneDoc(t, doc)
			if _, err := Apply(doc, []Spec{{Tag: "sub", Binding: b, Group: true}}); err == nil {
				t.Fatal("unsafe graph accepted")
			}
			if !sameJSON(doc, before) {
				t.Fatal("failed mutation changed document")
			}
		})
	}
}

func TestVerificationRejectsUnboundEndpointDNS(t *testing.T) {
	out, err := Apply(fixture(), []Spec{{Tag: "sub", Binding: binding(), Group: true}})
	if err != nil {
		t.Fatal(err)
	}
	delete(out["dns"].(map[string]any)["servers"].([]any)[1].(map[string]any), "bind_interface")
	if err = Verify(out, "sub", "wan0", true); err == nil {
		t.Fatal("unbound DNS accepted")
	}
}

func TestDomainToIPRemovesOnlyOwnedResolver(t *testing.T) {
	out, err := Apply(fixture(), []Spec{{Tag: "sub", Binding: binding(), Group: true}})
	if err != nil {
		t.Fatal(err)
	}
	node(out)["server"] = "203.0.113.1"
	out, err = Apply(out, []Spec{{Tag: "sub", Binding: binding(), Group: true}})
	if err != nil {
		t.Fatal(err)
	}
	servers := out["dns"].(map[string]any)["servers"].([]any)
	if len(servers) != 1 || !reflect.DeepEqual(servers[0].(map[string]any)["tag"], "user-dns") {
		t.Fatal(servers)
	}
	if err = Verify(out, "sub", "wan0", true); err != nil {
		t.Fatal(err)
	}
}

func TestIPv6OnlyWANUsesIPv6DNSAndRejectsIPv4Endpoint(t *testing.T) {
	b := uplink.Binding{Device: "wan6", IPv6: "2001:db8::2", DNSServers: []string{"[2001:db8::53]:5353"}}
	out, err := Apply(fixture(), []Spec{{Tag: "sub", Binding: b, Group: true}})
	if err != nil {
		t.Fatal(err)
	}
	if err = Verify(out, "sub", "wan6", true); err != nil {
		t.Fatal(err)
	}
	resolver := out["dns"].(map[string]any)["servers"].([]any)[1].(map[string]any)
	if resolver["inet6_bind_address"] != "2001:db8::2" || resolver["server"] != "2001:db8::53" {
		t.Fatal(resolver)
	}
	if node(out)["domain_resolver"].(map[string]any)["strategy"] != "ipv6_only" {
		t.Fatal(node(out))
	}
	node(out)["server"] = "203.0.113.1"
	if _, err = Apply(out, []Spec{{Tag: "sub", Binding: b, Group: true}}); err == nil {
		t.Fatal("IPv4 endpoint accepted without IPv4 source")
	}
}
