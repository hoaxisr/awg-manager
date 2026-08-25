package wdttlink

import (
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

const sampleEventLog = `
[КЛИЕНТ] Слушаю: 127.0.0.1:9000
__WDTT_EVENT__|CONFIG|{"config":"[Interface]\nPrivateKey = abc\nAddress = 10.0.0.2/32\n\n[Peer]\nPublicKey = xyz\nAllowedIPs = 0.0.0.0/0\n"}
`

const sampleBoxLog = `
╔══════════════ WireGuard Конфиг ══════════════╗
║ [Interface]                                  ║
║ PrivateKey = abc                             ║
║ Address = 10.0.0.2/32                        ║
║ [Peer]                                       ║
║ PublicKey = xyz                              ║
║ AllowedIPs = 0.0.0.0/0                       ║
╚══════════════════════════════════════════════╝
`

func TestExtractWGConfigFromLog_Event(t *testing.T) {
	got := ExtractWGConfigFromLog(sampleEventLog)
	if !looksLikeWGConf(got) {
		t.Fatalf("got=%q", got)
	}
	if !strings.Contains(got, "PrivateKey = abc") {
		t.Fatalf("missing private key: %q", got)
	}
}

func TestExtractWGConfigFromLog_Box(t *testing.T) {
	got := ExtractWGConfigFromLog(sampleBoxLog)
	if !looksLikeWGConf(got) {
		t.Fatalf("got=%q", got)
	}
}
func TestExtractPeerPublicKey(t *testing.T) {
	conf := "[Interface]\nPrivateKey = x\n\n[Peer]\nPublicKey = abc123=\n"
	if got := ExtractPeerPublicKey(conf); got != "abc123=" {
		t.Fatalf("got=%q", got)
	}
}

func TestPeersEqual(t *testing.T) {
	if !PeersEqual("1.2.3.4", "1.2.3.4:56000") {
		t.Fatal("expected equal with default port")
	}
	if PeersEqual("1.2.3.4:56000", "5.6.7.8:56000") {
		t.Fatal("expected different peers")
	}
}

func TestMatchingAWGTunnel(t *testing.T) {
	conf := "[Interface]\nPrivateKey = x\nAddress = 10.8.0.5/32\n\n[Peer]\nPublicKey = abc123=\n"

	// Same interface address but a different peer key must NOT match
	// (не усыновляем чужой туннель по совпавшему 10.x-адресу).
	tunnels := []storage.AWGTunnel{{
		ID:        "t1",
		Name:      "Германия wdtt",
		Peer:      storage.AWGPeer{PublicKey: "other="},
		Interface: storage.AWGInterface{Address: "10.8.0.5/32"},
	}}
	if got := MatchingAWGTunnel(tunnels, conf); got != nil {
		t.Fatalf("expected nil for addr-only overlap, got %v", got)
	}

	// Matching peer key (different address) → match.
	tunnels[0].Interface.Address = "10.9.0.1/32"
	tunnels[0].Peer.PublicKey = "abc123="
	if got := MatchingAWGTunnel(tunnels, conf); got == nil || got.ID != "t1" {
		t.Fatalf("expected match by pubkey, got %v", got)
	}

	// Empty config → no match, no panic.
	if got := MatchingAWGTunnel(tunnels, ""); got != nil {
		t.Fatalf("expected nil for empty conf, got %v", got)
	}
}

func TestPatchWgConfEndpoint(t *testing.T) {
	in := "[Interface]\nPrivateKey = x\n\n[Peer]\nPublicKey = y\nAllowedIPs = 0.0.0.0/0\n"
	got := PatchWgConfEndpoint(in, 9000)
	if !strings.Contains(got, "Endpoint = 127.0.0.1:9000") {
		t.Fatalf("got=%q", got)
	}
}
