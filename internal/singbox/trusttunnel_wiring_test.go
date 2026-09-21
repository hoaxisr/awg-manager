package singbox

import (
	"strings"
	"testing"
)

const ttWireTOML = "hostname = \"vpn.example.com\"\naddresses = [\"1.2.3.4:443\"]\nusername = \"premium\"\npassword = \"s3cretPass\"\n"

func TestParseTunnelLinksInput_TrustTunnelTOML(t *testing.T) {
	res := ParseTunnelLinksInput(ttWireTOML)
	if len(res.Outbounds) != 1 || res.Outbounds[0].Protocol != "trusttunnel" {
		t.Fatalf("res: %+v", res)
	}
}

func TestOutboundRequiresFeature_TrustTunnel(t *testing.T) {
	if got := OutboundTypeRequiresFeature("trusttunnel"); got != "with_trusttunnel" {
		t.Fatalf("got %q", got)
	}
	if OutboundSupportedByFeatures([]string{"with_quic", "with_utls"}, "trusttunnel") {
		t.Fatal("binary without tag must not support trusttunnel")
	}
	if !OutboundSupportedByFeatures([]string{"with_trusttunnel"}, "trusttunnel") {
		t.Fatal("binary with tag must support trusttunnel")
	}
}

func TestOutboundFingerprint_TrustTunnel(t *testing.T) {
	ob := map[string]any{"type": "trusttunnel", "server": "1.2.3.4", "server_port": 443, "username": "u", "password": "p"}
	if fp := outboundFingerprint(ob); !strings.HasPrefix(fp, "trusttunnel|1.2.3.4|443|u:p") {
		t.Fatalf("fp %q", fp)
	}
}

func TestDetectTransport_TrustTunnel(t *testing.T) {
	if got := detectTransport(map[string]any{"type": "trusttunnel"}); got != "https" {
		t.Fatalf("got %q", got)
	}
}
