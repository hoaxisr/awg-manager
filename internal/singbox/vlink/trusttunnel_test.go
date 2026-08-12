package vlink

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

const trustTunnelConnectURL = "https://trustunnel.ru/connect/?d=ARF1czMudHJ1dHVuLm9ubGluZQUPdXNlcl8xMzUzODE4OTc5Bgw4ZU9wclZwYXhReDYCFXVzMy50cnV0dW4ub25saW5lOjQ0MwsIZjcwMDQ4YWYDEXVzMy50cnV0dW4ub25saW5lDB_wn4e68J-HuCBVU0EgKNCh0KjQkCkgKFByZW1pdW0pDUBBJWh0dHBzOi8vZG5zLmFkZ3VhcmQtZG5zLmNvbS9kbnMtcXVlcnkacXVpYzovL2Rucy5hZGd1YXJkLWRucy5jb20EAQAKAQEJAQI"

func TestParseTrustTunnelConnectURL(t *testing.T) {
	parsed, err := parseTrustTunnelConnectURL(trustTunnelConnectURL)
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed) != 1 {
		t.Fatalf("expected 1 outbound, got %d", len(parsed))
	}
	ob := parsed[0]
	if ob.Protocol != "trusttunnel" {
		t.Fatalf("protocol: %q", ob.Protocol)
	}
	if ob.Server != "us3.trutun.online" || ob.Port != 443 {
		t.Fatalf("server/port: %s:%d", ob.Server, ob.Port)
	}
	var m map[string]any
	if err := json.Unmarshal(ob.Outbound, &m); err != nil {
		t.Fatal(err)
	}
	if m["username"] != "user_1353818979" || m["password"] != "8eOprVpaxQx6" {
		t.Fatalf("auth: %#v", m)
	}
	if m["quic"] != false {
		t.Fatalf("expected quic=false (H2 workaround), got %#v", m["quic"])
	}
	if m["client_random_prefix"] != "f70048af" {
		t.Fatalf("client_random_prefix: %#v", m["client_random_prefix"])
	}
	tls, _ := m["tls"].(map[string]any)
	if tls["server_name"] != "us3.trutun.online" {
		t.Fatalf("tls sni: %#v", tls)
	}
}

func TestParseTrustTunnelClientTOMLFile(t *testing.T) {
	path := `c:\Users\Ivan\Downloads\Telegram Desktop\tt_1353818979 (11).toml`
	body, err := os.ReadFile(path)
	if err != nil {
		t.Skip("sample TOML not available:", err)
	}
	if !IsTrustTunnelClientTOML(body) {
		t.Fatal("IsTrustTunnelClientTOML=false")
	}
	res := ParseTrustTunnelClientTOML(body)
	if len(res.Errors) > 0 {
		t.Fatalf("errors: %v", res.Errors)
	}
	if len(res.Outbounds) != 1 {
		t.Fatalf("expected 1 outbound, got %d", len(res.Outbounds))
	}
	var m map[string]any
	if err := json.Unmarshal(res.Outbounds[0].Outbound, &m); err != nil {
		t.Fatal(err)
	}
	if m["username"] != "user_1353818979" || m["password"] != "8eOprVpaxQx6" {
		t.Fatalf("auth: %#v", m)
	}
	if m["quic"] != false {
		t.Fatalf("expected quic=false for H2 workaround")
	}
	if m["client_random_prefix"] != "f70048af" {
		t.Fatalf("client_random_prefix: %#v", m["client_random_prefix"])
	}
}

func TestParseLinkManyTrustTunnelConnect(t *testing.T) {
	parsed, err := ParseLinkMany(trustTunnelConnectURL)
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed) != 1 {
		t.Fatalf("got %d outbounds", len(parsed))
	}
}

func TestParseTrustTunnelTLVFields(t *testing.T) {
	const payload = "ARF1czMudHJ1dHVuLm9ubGluZQUPdXNlcl8xMzUzODE4OTc5Bgw4ZU9wclZwYXhReDYCFXVzMy50cnV0dW4ub25saW5lOjQ0MwsIZjcwMDQ4YWYDEXVzMy50cnV0dW4ub25saW5lDB_wn4e68J-HuCBVU0EgKNCh0KjQkCkgKFByZW1pdW0pDUBBJWh0dHBzOi8vZG5zLmFkZ3VhcmQtZG5zLmNvbS9kbnMtcXVlcnkacXVpYzovL2Rucy5hZGd1YXJkLWRucy5jb20EAQAKAQEJAQI"
	ep, err := decodeTrustTunnelPayload(payload)
	if err != nil {
		t.Fatal(err)
	}
	if ep.Hostname != "us3.trutun.online" {
		t.Fatalf("hostname: %q", ep.Hostname)
	}
	if ep.Username != "user_1353818979" || ep.Password != "8eOprVpaxQx6" {
		t.Fatalf("credentials mismatch")
	}
	if ep.UpstreamProtocol != "http3" {
		t.Fatalf("upstream: %q", ep.UpstreamProtocol)
	}
	if !ep.AntiDPI || ep.ClientRandomPrefix != "f70048af" {
		t.Fatalf("anti_dpi/prefix: %v %q", ep.AntiDPI, ep.ClientRandomPrefix)
	}
	if ep.Name != "🇺🇸 USA (США) (Premium)" {
		t.Fatalf("name: %q", ep.Name)
	}
	if len(ep.DNSUpstreams) != 2 || !strings.HasPrefix(ep.DNSUpstreams[0], "https://") {
		t.Fatalf("dns: %#v", ep.DNSUpstreams)
	}
}
