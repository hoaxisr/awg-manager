package configmerge

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeJSON(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func TestMerge_TwoFilesConcatInbounds(t *testing.T) {
	dir := t.TempDir()
	writeJSON(t, dir, "00-base.json", `{"inbounds":[{"tag":"a","type":"http"}]}`)
	writeJSON(t, dir, "10-tunnels.json", `{"inbounds":[{"tag":"b","type":"socks"}]}`)

	out, err := MergeDir(dir)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if !strings.Contains(out, `"tag": "a"`) || !strings.Contains(out, `"tag": "b"`) {
		t.Errorf("merged output missing one of the inbound tags:\n%s", out)
	}
}

func TestMerge_TagCollision_Outbounds(t *testing.T) {
	dir := t.TempDir()
	writeJSON(t, dir, "10-a.json", `{"outbounds":[{"tag":"veesp","type":"vless"}]}`)
	writeJSON(t, dir, "20-b.json", `{"outbounds":[{"tag":"veesp","type":"direct"}]}`)

	_, err := MergeDir(dir)
	if err == nil {
		t.Fatal("expected collision error, got nil")
	}
	ce, ok := err.(*CollisionError)
	if !ok {
		t.Fatalf("expected *CollisionError, got %T: %v", err, err)
	}
	if ce.Tag != "veesp" || ce.Kind != "outbounds" {
		t.Errorf("unexpected collision details: %+v", ce)
	}
	if ce.FirstFile != "10-a.json" || ce.SecondFile != "20-b.json" {
		t.Errorf("unexpected file names: %+v", ce)
	}
}

func TestMerge_TagCollision_DnsServers(t *testing.T) {
	dir := t.TempDir()
	writeJSON(t, dir, "00-base.json", `{"dns":{"servers":[{"tag":"quad","type":"tls"}]}}`)
	writeJSON(t, dir, "20-router.json", `{"dns":{"servers":[{"tag":"quad","type":"udp"}]}}`)

	_, err := MergeDir(dir)
	if err == nil {
		t.Fatal("expected collision error, got nil")
	}
	ce, _ := err.(*CollisionError)
	if ce == nil || ce.Kind != "servers" || ce.Tag != "quad" {
		t.Errorf("expected dns.servers tag collision, got %v", err)
	}
}

func TestMerge_NestedObject_LastWriterWins(t *testing.T) {
	dir := t.TempDir()
	writeJSON(t, dir, "00-base.json", `{"log":{"level":"info","timestamp":false}}`)
	writeJSON(t, dir, "20-router.json", `{"log":{"level":"trace"}}`)

	out, err := MergeDir(dir)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	// Last writer wins for level; timestamp survives from base.
	if !contains(out, `"level": "trace"`) {
		t.Errorf("expected level=trace in output:\n%s", out)
	}
	if !contains(out, `"timestamp": false`) {
		t.Errorf("expected timestamp from base to survive:\n%s", out)
	}
}

func TestMerge_RouteRulesConcat(t *testing.T) {
	dir := t.TempDir()
	writeJSON(t, dir, "00-base.json", `{"route":{"rules":[{"action":"sniff"}]}}`)
	writeJSON(t, dir, "20-router.json", `{"route":{"rules":[{"action":"hijack-dns"}]}}`)

	out, err := MergeDir(dir)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if !contains(out, `"sniff"`) || !contains(out, `"hijack-dns"`) {
		t.Errorf("expected both route.rules entries:\n%s", out)
	}
}

func TestMerge_DNSFinal_FirstFileWins(t *testing.T) {
	dir := t.TempDir()
	// Mirror the #445 scenario: base sets dns.final first, router second.
	writeJSON(t, dir, "00-base.json", `{"dns":{"final":"dns-bootstrap","strategy":"prefer_ipv4"}}`)
	writeJSON(t, dir, "20-router.json", `{"dns":{"final":"dns-direct","strategy":"ipv4_only"}}`)

	out, err := MergeDir(dir)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	// sing-box keeps the lexically-first file's scalar dns sub-keys.
	if !contains(out, `"final": "dns-bootstrap"`) {
		t.Errorf("expected first-file dns.final (dns-bootstrap) to win:\n%s", out)
	}
	if !contains(out, `"strategy": "prefer_ipv4"`) {
		t.Errorf("expected first-file dns.strategy (prefer_ipv4) to win:\n%s", out)
	}
	if contains(out, `"dns-direct"`) {
		t.Errorf("second-file dns.final must not appear:\n%s", out)
	}
}

func TestMerge_RouteFinal_FirstFileWins(t *testing.T) {
	dir := t.TempDir()
	writeJSON(t, dir, "00-base.json", `{"route":{"final":"direct"}}`)
	writeJSON(t, dir, "20-router.json", `{"route":{"final":"myproxy"}}`)

	out, err := MergeDir(dir)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if !contains(out, `"final": "direct"`) {
		t.Errorf("expected first-file route.final (direct) to win:\n%s", out)
	}
	if contains(out, `"myproxy"`) {
		t.Errorf("second-file route.final must not appear:\n%s", out)
	}
}

func TestMerge_DNSRules_StillConcat(t *testing.T) {
	dir := t.TempDir()
	writeJSON(t, dir, "00-base.json", `{"dns":{"rules":[{"server":"a"}]}}`)
	writeJSON(t, dir, "20-router.json", `{"dns":{"rules":[{"server":"b"}]}}`)

	out, err := MergeDir(dir)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if !contains(out, `"server": "a"`) || !contains(out, `"server": "b"`) {
		t.Errorf("dns.rules arrays must still concatenate:\n%s", out)
	}
}

func TestMerge_SkipsDisabledSubdir(t *testing.T) {
	dir := t.TempDir()
	writeJSON(t, dir, "20-router.json", `{"inbounds":[{"tag":"active","type":"http"}]}`)
	disabled := filepath.Join(dir, "disabled")
	if err := os.MkdirAll(disabled, 0755); err != nil {
		t.Fatal(err)
	}
	writeJSON(t, disabled, "30-deviceproxy.json", `{"inbounds":[{"tag":"parked","type":"socks"}]}`)

	out, err := MergeDir(dir)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if !contains(out, `"active"`) {
		t.Errorf("expected active inbound:\n%s", out)
	}
	if contains(out, `"parked"`) {
		t.Errorf("disabled inbound must not appear:\n%s", out)
	}
}

func TestMerge_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	out, err := MergeDir(dir)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	// Empty dir → empty merged JSON object.
	if out != "{}" {
		t.Errorf("expected '{}', got %q", out)
	}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

func TestMerge_HTTPClientsConcatAndCollide(t *testing.T) {
	dir := t.TempDir()
	writeJSON(t, dir, "20-router.json", `{"http_clients":[{"tag":"rs-download","detour":"vpn"}]}`)
	writeJSON(t, dir, "90-user.json", `{"http_clients":[{"tag":"mine"}]}`)
	out, err := MergeDir(dir)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if !strings.Contains(out, `"tag": "rs-download"`) || !strings.Contains(out, `"tag": "mine"`) {
		t.Errorf("http_clients not concatenated:\n%s", out)
	}

	writeJSON(t, dir, "90-user.json", `{"http_clients":[{"tag":"rs-download"}]}`)
	_, err = MergeDir(dir)
	ce, ok := err.(*CollisionError)
	if !ok || ce.Kind != "http_clients" || ce.Tag != "rs-download" {
		t.Fatalf("want http_clients collision, got %v", err)
	}
}
