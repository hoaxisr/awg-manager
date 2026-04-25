package config

import (
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// --- Parse tests ---

func TestParse_BasicWireGuardConfig(t *testing.T) {
	conf := `[Interface]
PrivateKey = aPrivateKey123=
Address = 10.0.0.2/32
MTU = 1400

[Peer]
PublicKey = aPublicKey456=
Endpoint = vpn.example.com:51820
AllowedIPs = 0.0.0.0/0, ::/0
PersistentKeepalive = 30
`
	tunnel, err := Parse(conf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if tunnel.Type != "awg" {
		t.Errorf("Type = %q, want %q", tunnel.Type, "awg")
	}
	if tunnel.Interface.PrivateKey != "aPrivateKey123=" {
		t.Errorf("PrivateKey = %q, want %q", tunnel.Interface.PrivateKey, "aPrivateKey123=")
	}
	if tunnel.Interface.Address != "10.0.0.2/32" {
		t.Errorf("Address = %q, want %q", tunnel.Interface.Address, "10.0.0.2/32")
	}
	if tunnel.Interface.MTU != 1400 {
		t.Errorf("MTU = %d, want %d", tunnel.Interface.MTU, 1400)
	}
	if tunnel.Peer.PublicKey != "aPublicKey456=" {
		t.Errorf("PublicKey = %q, want %q", tunnel.Peer.PublicKey, "aPublicKey456=")
	}
	if tunnel.Peer.Endpoint != "vpn.example.com:51820" {
		t.Errorf("Endpoint = %q, want %q", tunnel.Peer.Endpoint, "vpn.example.com:51820")
	}
	if tunnel.Peer.PersistentKeepalive != 30 {
		t.Errorf("PersistentKeepalive = %d, want %d", tunnel.Peer.PersistentKeepalive, 30)
	}
	if len(tunnel.Peer.AllowedIPs) != 2 {
		t.Fatalf("AllowedIPs len = %d, want 2", len(tunnel.Peer.AllowedIPs))
	}
	if tunnel.Peer.AllowedIPs[0] != "0.0.0.0/0" || tunnel.Peer.AllowedIPs[1] != "::/0" {
		t.Errorf("AllowedIPs = %v, want [0.0.0.0/0 ::/0]", tunnel.Peer.AllowedIPs)
	}
}

func TestParse_AWG10_FullObfuscation(t *testing.T) {
	conf := `[Interface]
PrivateKey = privkey=
Address = 10.8.0.2/32
MTU = 1280
Jc = 4
Jmin = 50
Jmax = 1000
S1 = 56
S2 = 78
S3 = 0
S4 = 0
H1 = 123456
H2 = 789012
H3 = 345678
H4 = 901234

[Peer]
PublicKey = pubkey=
Endpoint = 1.2.3.4:51820
AllowedIPs = 0.0.0.0/0
PersistentKeepalive = 25
`
	tunnel, err := Parse(conf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	iface := tunnel.Interface
	if iface.Jc != 4 {
		t.Errorf("Jc = %d, want 4", iface.Jc)
	}
	if iface.Jmin != 50 {
		t.Errorf("Jmin = %d, want 50", iface.Jmin)
	}
	if iface.Jmax != 1000 {
		t.Errorf("Jmax = %d, want 1000", iface.Jmax)
	}
	if iface.S1 != 56 {
		t.Errorf("S1 = %d, want 56", iface.S1)
	}
	if iface.S2 != 78 {
		t.Errorf("S2 = %d, want 78", iface.S2)
	}
	// S3=0 is valid but won't be distinguishable from default zero
	if iface.H1 != "123456" {
		t.Errorf("H1 = %q, want %q", iface.H1, "123456")
	}
	if iface.H2 != "789012" {
		t.Errorf("H2 = %q, want %q", iface.H2, "789012")
	}
	if iface.H3 != "345678" {
		t.Errorf("H3 = %q, want %q", iface.H3, "345678")
	}
	if iface.H4 != "901234" {
		t.Errorf("H4 = %q, want %q", iface.H4, "901234")
	}
}

func TestParse_AWG15_WithSignaturePackets(t *testing.T) {
	conf := `[Interface]
PrivateKey = privkey=
Address = 10.0.0.2/32
Jc = 4
Jmin = 50
Jmax = 1000
S1 = 56
S2 = 78
H1 = 111
H2 = 222
H3 = 333
H4 = 444
I1 = AABBCCDD
I2 = 11223344
I3 = DEADBEEF
I4 = CAFEBABE
I5 = F00DCAFE

[Peer]
PublicKey = pubkey=
Endpoint = server:51820
`
	tunnel, err := Parse(conf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	iface := tunnel.Interface
	if iface.I1 != "AABBCCDD" {
		t.Errorf("I1 = %q, want %q", iface.I1, "AABBCCDD")
	}
	if iface.I2 != "11223344" {
		t.Errorf("I2 = %q, want %q", iface.I2, "11223344")
	}
	if iface.I3 != "DEADBEEF" {
		t.Errorf("I3 = %q, want %q", iface.I3, "DEADBEEF")
	}
	if iface.I4 != "CAFEBABE" {
		t.Errorf("I4 = %q, want %q", iface.I4, "CAFEBABE")
	}
	if iface.I5 != "F00DCAFE" {
		t.Errorf("I5 = %q, want %q", iface.I5, "F00DCAFE")
	}
}

func TestParse_DefaultsWhenMissing(t *testing.T) {
	conf := `[Interface]
PrivateKey = privkey=
Address = 10.0.0.2/32

[Peer]
PublicKey = pubkey=
Endpoint = server:51820
`
	tunnel, err := Parse(conf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if tunnel.Interface.MTU != DefaultMTU {
		t.Errorf("MTU = %d, want default %d", tunnel.Interface.MTU, DefaultMTU)
	}
	if tunnel.Peer.PersistentKeepalive != DefaultPersistentKeepalive {
		t.Errorf("PersistentKeepalive = %d, want default %d",
			tunnel.Peer.PersistentKeepalive, DefaultPersistentKeepalive)
	}
	if len(tunnel.Peer.AllowedIPs) != 2 {
		t.Fatalf("AllowedIPs len = %d, want 2 (defaults)", len(tunnel.Peer.AllowedIPs))
	}
	if tunnel.Peer.AllowedIPs[0] != "0.0.0.0/0" || tunnel.Peer.AllowedIPs[1] != "::/0" {
		t.Errorf("AllowedIPs = %v, want default [0.0.0.0/0 ::/0]", tunnel.Peer.AllowedIPs)
	}
}

func TestParse_CommentsAndEmptyLines(t *testing.T) {
	conf := `# This is a comment
[Interface]
# Private key below
PrivateKey = privkey=
Address = 10.0.0.2/32

# Empty lines above and below

[Peer]
PublicKey = pubkey=
# Endpoint comment
Endpoint = server:51820
`
	tunnel, err := Parse(conf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if tunnel.Interface.PrivateKey != "privkey=" {
		t.Errorf("PrivateKey = %q, want %q", tunnel.Interface.PrivateKey, "privkey=")
	}
	if tunnel.Peer.PublicKey != "pubkey=" {
		t.Errorf("PublicKey = %q, want %q", tunnel.Peer.PublicKey, "pubkey=")
	}
}

func TestParse_CaseInsensitiveSections(t *testing.T) {
	conf := `[INTERFACE]
PrivateKey = privkey=
Address = 10.0.0.2/32

[PEER]
PublicKey = pubkey=
Endpoint = server:51820
`
	tunnel, err := Parse(conf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if tunnel.Interface.PrivateKey != "privkey=" {
		t.Errorf("PrivateKey = %q, want %q", tunnel.Interface.PrivateKey, "privkey=")
	}
}

func TestParse_CaseInsensitiveKeys(t *testing.T) {
	conf := `[Interface]
PRIVATEKEY = privkey=
ADDRESS = 10.0.0.1/24
mtu = 1420

[Peer]
PUBLICKEY = pubkey=
ENDPOINT = server:51820
allowedips = 10.0.0.0/24
PERSISTENTKEEPALIVE = 15
`
	tunnel, err := Parse(conf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if tunnel.Interface.PrivateKey != "privkey=" {
		t.Errorf("PrivateKey = %q", tunnel.Interface.PrivateKey)
	}
	if tunnel.Interface.Address != "10.0.0.1/24" {
		t.Errorf("Address = %q", tunnel.Interface.Address)
	}
	if tunnel.Interface.MTU != 1420 {
		t.Errorf("MTU = %d, want 1420", tunnel.Interface.MTU)
	}
	if tunnel.Peer.PersistentKeepalive != 15 {
		t.Errorf("PersistentKeepalive = %d, want 15", tunnel.Peer.PersistentKeepalive)
	}
}

func TestParse_PresharedKey(t *testing.T) {
	conf := `[Interface]
PrivateKey = privkey=
Address = 10.0.0.2/32

[Peer]
PublicKey = pubkey=
PresharedKey = psk123=
Endpoint = server:51820
`
	tunnel, err := Parse(conf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if tunnel.Peer.PresharedKey != "psk123=" {
		t.Errorf("PresharedKey = %q, want %q", tunnel.Peer.PresharedKey, "psk123=")
	}
}

func TestParse_AllowedIPsMultiple(t *testing.T) {
	conf := `[Interface]
PrivateKey = privkey=
Address = 10.0.0.2/32

[Peer]
PublicKey = pubkey=
Endpoint = server:51820
AllowedIPs = 10.0.0.0/24, 192.168.1.0/24, 172.16.0.0/12
`
	tunnel, err := Parse(conf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := []string{"10.0.0.0/24", "192.168.1.0/24", "172.16.0.0/12"}
	if len(tunnel.Peer.AllowedIPs) != len(want) {
		t.Fatalf("AllowedIPs len = %d, want %d", len(tunnel.Peer.AllowedIPs), len(want))
	}
	for i, ip := range tunnel.Peer.AllowedIPs {
		if ip != want[i] {
			t.Errorf("AllowedIPs[%d] = %q, want %q", i, ip, want[i])
		}
	}
}

func TestParse_LinesBeforeSection(t *testing.T) {
	// Lines before any section header should be silently ignored
	conf := `SomeKey = SomeValue
[Interface]
PrivateKey = privkey=
Address = 10.0.0.2/32

[Peer]
PublicKey = pubkey=
Endpoint = server:51820
`
	tunnel, err := Parse(conf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tunnel.Interface.PrivateKey != "privkey=" {
		t.Errorf("PrivateKey = %q, want %q", tunnel.Interface.PrivateKey, "privkey=")
	}
}

// --- Parse error tests ---

func TestParse_ErrMissingPrivateKey(t *testing.T) {
	conf := `[Interface]
Address = 10.0.0.2/32

[Peer]
PublicKey = pubkey=
Endpoint = server:51820
`
	_, err := Parse(conf)
	if err != ErrMissingPrivateKey {
		t.Errorf("err = %v, want ErrMissingPrivateKey", err)
	}
}

func TestParse_ErrMissingAddress(t *testing.T) {
	conf := `[Interface]
PrivateKey = privkey=

[Peer]
PublicKey = pubkey=
Endpoint = server:51820
`
	_, err := Parse(conf)
	if err != ErrMissingAddress {
		t.Errorf("err = %v, want ErrMissingAddress", err)
	}
}

func TestParse_ErrMissingPublicKey(t *testing.T) {
	conf := `[Interface]
PrivateKey = privkey=
Address = 10.0.0.2/32

[Peer]
Endpoint = server:51820
`
	_, err := Parse(conf)
	if err != ErrMissingPublicKey {
		t.Errorf("err = %v, want ErrMissingPublicKey", err)
	}
}

func TestParse_ErrMissingEndpoint(t *testing.T) {
	conf := `[Interface]
PrivateKey = privkey=
Address = 10.0.0.2/32

[Peer]
PublicKey = pubkey=
`
	_, err := Parse(conf)
	if err != ErrMissingEndpoint {
		t.Errorf("err = %v, want ErrMissingEndpoint", err)
	}
}

func TestParse_ErrMultiplePeers(t *testing.T) {
	conf := `[Interface]
PrivateKey = privkey=

[Peer]
PublicKey = pubkey1=
Endpoint = server1:51820

[Peer]
PublicKey = pubkey2=
Endpoint = server2:51820
`
	_, err := Parse(conf)
	if err != ErrMultiplePeers {
		t.Errorf("err = %v, want ErrMultiplePeers", err)
	}
}

func TestParse_EmptyConfig(t *testing.T) {
	_, err := Parse("")
	if err != ErrMissingPrivateKey {
		t.Errorf("err = %v, want ErrMissingPrivateKey", err)
	}
}

func TestParse_InvalidMTU_Ignored(t *testing.T) {
	conf := `[Interface]
PrivateKey = privkey=
Address = 10.0.0.2/32
MTU = notanumber

[Peer]
PublicKey = pubkey=
Endpoint = server:51820
`
	tunnel, err := Parse(conf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Invalid MTU should be ignored, default applied
	if tunnel.Interface.MTU != DefaultMTU {
		t.Errorf("MTU = %d, want default %d", tunnel.Interface.MTU, DefaultMTU)
	}
}

func TestParse_ValueWithEqualsSign(t *testing.T) {
	// Base64 keys often contain '=' — make sure we only split on the first '='
	conf := `[Interface]
PrivateKey = abc123def456+/=
Address = 10.0.0.2/32

[Peer]
PublicKey = xyz789ghi012+/==
Endpoint = server:51820
`
	tunnel, err := Parse(conf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if tunnel.Interface.PrivateKey != "abc123def456+/=" {
		t.Errorf("PrivateKey = %q, want %q", tunnel.Interface.PrivateKey, "abc123def456+/=")
	}
	if tunnel.Peer.PublicKey != "xyz789ghi012+/==" {
		t.Errorf("PublicKey = %q, want %q", tunnel.Peer.PublicKey, "xyz789ghi012+/==")
	}
}

// --- Generate tests ---

func TestGenerate_BasicConfig(t *testing.T) {
	tunnel := &storage.AWGTunnel{
		Interface: storage.AWGInterface{
			PrivateKey: "privkey=",
		},
		Peer: storage.AWGPeer{
			PublicKey:           "pubkey=",
			Endpoint:            "server:51820",
			AllowedIPs:          []string{"0.0.0.0/0", "::/0"},
			PersistentKeepalive: 25,
		},
	}

	result := Generate(tunnel)

	assertContains(t, result, "PrivateKey = privkey=")
	assertContains(t, result, "PublicKey = pubkey=")
	assertContains(t, result, "Endpoint = server:51820")
	assertContains(t, result, "AllowedIPs = 0.0.0.0/0, ::/0")
	assertContains(t, result, "PersistentKeepalive = 25")
	assertContains(t, result, "[Interface]")
	assertContains(t, result, "[Peer]")
}

func TestGenerate_WithObfuscation(t *testing.T) {
	tunnel := &storage.AWGTunnel{
		Interface: storage.AWGInterface{
			PrivateKey: "privkey=",
			AWGObfuscation: storage.AWGObfuscation{
				Jc:   4,
				Jmin: 50,
				Jmax: 1000,
				S1:   56,
				S2:   78,
				H1:   "111",
				H2:   "222",
				H3:   "333",
				H4:   "444",
			},
		},
		Peer: storage.AWGPeer{
			PublicKey:           "pubkey=",
			Endpoint:            "server:51820",
			AllowedIPs:          []string{"0.0.0.0/0"},
			PersistentKeepalive: 25,
		},
	}

	result := Generate(tunnel)

	assertContains(t, result, "Jc = 4")
	assertContains(t, result, "Jmin = 50")
	assertContains(t, result, "Jmax = 1000")
	assertContains(t, result, "S1 = 56")
	assertContains(t, result, "S2 = 78")
	assertContains(t, result, "H1 = 111")
	assertContains(t, result, "H2 = 222")
	assertContains(t, result, "H3 = 333")
	assertContains(t, result, "H4 = 444")
	// S3, S4 are 0, should NOT appear
	assertNotContains(t, result, "S3 =")
	assertNotContains(t, result, "S4 =")
}

func TestGenerate_WithSignaturePackets(t *testing.T) {
	tunnel := &storage.AWGTunnel{
		Interface: storage.AWGInterface{
			PrivateKey: "privkey=",
			AWGObfuscation: storage.AWGObfuscation{
				I1: "AABB",
				I2: "CCDD",
				I3: "EEFF",
				I4: "0011",
				I5: "2233",
			},
		},
		Peer: storage.AWGPeer{
			PublicKey:           "pubkey=",
			Endpoint:            "server:51820",
			AllowedIPs:          []string{"0.0.0.0/0"},
			PersistentKeepalive: 25,
		},
	}

	result := Generate(tunnel)

	assertContains(t, result, "I1 = AABB")
	assertContains(t, result, "I2 = CCDD")
	assertContains(t, result, "I3 = EEFF")
	assertContains(t, result, "I4 = 0011")
	assertContains(t, result, "I5 = 2233")
}

func TestGenerate_PresharedKey(t *testing.T) {
	tunnel := &storage.AWGTunnel{
		Interface: storage.AWGInterface{
			PrivateKey: "privkey=",
		},
		Peer: storage.AWGPeer{
			PublicKey:           "pubkey=",
			PresharedKey:        "psk=",
			Endpoint:            "server:51820",
			AllowedIPs:          []string{"0.0.0.0/0"},
			PersistentKeepalive: 25,
		},
	}

	result := Generate(tunnel)
	assertContains(t, result, "PresharedKey = psk=")
}

func TestGenerate_NoPresharedKey(t *testing.T) {
	tunnel := &storage.AWGTunnel{
		Interface: storage.AWGInterface{
			PrivateKey: "privkey=",
		},
		Peer: storage.AWGPeer{
			PublicKey:           "pubkey=",
			Endpoint:            "server:51820",
			AllowedIPs:          []string{"0.0.0.0/0"},
			PersistentKeepalive: 25,
		},
	}

	result := Generate(tunnel)
	assertNotContains(t, result, "PresharedKey")
}

func TestGenerate_DefaultAllowedIPs(t *testing.T) {
	tunnel := &storage.AWGTunnel{
		Interface: storage.AWGInterface{
			PrivateKey: "privkey=",
		},
		Peer: storage.AWGPeer{
			PublicKey:           "pubkey=",
			Endpoint:            "server:51820",
			AllowedIPs:          nil, // empty
			PersistentKeepalive: 25,
		},
	}

	result := Generate(tunnel)
	assertContains(t, result, "AllowedIPs = 0.0.0.0/0, ::/0")
}

func TestGenerate_DefaultKeepalive(t *testing.T) {
	tunnel := &storage.AWGTunnel{
		Interface: storage.AWGInterface{
			PrivateKey: "privkey=",
		},
		Peer: storage.AWGPeer{
			PublicKey:  "pubkey=",
			Endpoint:   "server:51820",
			AllowedIPs: []string{"0.0.0.0/0"},
			// PersistentKeepalive = 0
		},
	}

	result := Generate(tunnel)
	assertContains(t, result, "PersistentKeepalive = 25")
}

// --- Real-world AWG 2.0 config (user-reported issue) ---

func TestParse_AWG20_BinaryI1_WithRangeH(t *testing.T) {
	// Real-world config from AmneziaVPN with:
	// - H1-H4 as ranges (AWG 2.0)
	// - I1 as binary hex blob <b 0x...>
	// - Empty I2-I5
	conf := `[Interface]
Address = 10.8.1.2/32
DNS = 1.1.1.1, 1.0.0.1
PrivateKey = aFakePrivateKeyForTesting123456=
Jc = 5
Jmin = 10
Jmax = 50
S1 = 63
S2 = 138
S3 = 47
S4 = 19
H1 = 1635672874-1803270462
H2 = 2068218376-2096535092
H3 = 2136583098-2139864411
H4 = 2145353296-2146095616
I1 = <b 0x084481800001000300000000077469636b65747306776964676574096b696e6f706f69736b0272750000010001c00c0005000100000039001806776964676574077469636b6574730679616e646578c025c0390005000100000039002b1765787465726e616c2d7469636b6574732d776964676574066166697368610679616e646578036e657400c05d000100010000001c000457fafe25>
I2 =
I3 =
I4 =
I5 =

[Peer]
PublicKey = aFakePublicKeyForTesting7890123=
PresharedKey = aFakePresharedKeyTesting456789=
AllowedIPs = 0.0.0.0/0, ::/0
Endpoint = 203.0.113.1:31582
PersistentKeepalive = 25
`

	tunnel, err := Parse(conf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify AWG params
	if tunnel.Interface.Jc != 5 {
		t.Errorf("Jc = %d, want 5", tunnel.Interface.Jc)
	}
	if tunnel.Interface.S3 != 47 {
		t.Errorf("S3 = %d, want 47", tunnel.Interface.S3)
	}
	if tunnel.Interface.S4 != 19 {
		t.Errorf("S4 = %d, want 19", tunnel.Interface.S4)
	}

	// H-values must be preserved as exact range strings
	if tunnel.Interface.H1 != "1635672874-1803270462" {
		t.Errorf("H1 = %q, want %q", tunnel.Interface.H1, "1635672874-1803270462")
	}
	if tunnel.Interface.H4 != "2145353296-2146095616" {
		t.Errorf("H4 = %q, want %q", tunnel.Interface.H4, "2145353296-2146095616")
	}

	// I1 binary blob must be preserved exactly (including <b 0x...> wrapper)
	wantI1 := "<b 0x084481800001000300000000077469636b65747306776964676574096b696e6f706f69736b0272750000010001c00c0005000100000039001806776964676574077469636b6574730679616e646578c025c0390005000100000039002b1765787465726e616c2d7469636b6574732d776964676574066166697368610679616e646578036e657400c05d000100010000001c000457fafe25>"
	if tunnel.Interface.I1 != wantI1 {
		t.Errorf("I1 = %q,\nwant %q", tunnel.Interface.I1, wantI1)
	}

	// Empty I2-I5 should remain empty
	if tunnel.Interface.I2 != "" {
		t.Errorf("I2 = %q, want empty", tunnel.Interface.I2)
	}
	if tunnel.Interface.I3 != "" {
		t.Errorf("I3 = %q, want empty", tunnel.Interface.I3)
	}

	// DNS is now parsed and stored
	if tunnel.Interface.DNS != "1.1.1.1, 1.0.0.1" {
		t.Errorf("DNS = %q, want %q", tunnel.Interface.DNS, "1.1.1.1, 1.0.0.1")
	}
	// Address is parsed
	if tunnel.Interface.Address != "10.8.1.2/32" {
		t.Errorf("Address = %q, want %q", tunnel.Interface.Address, "10.8.1.2/32")
	}

	// Should classify as AWG 2.0
	version := ClassifyAWGVersion(&tunnel.Interface)
	if version != "awg2.0" {
		t.Errorf("ClassifyAWGVersion = %q, want %q", version, "awg2.0")
	}
}

func TestRoundtrip_AWG20_BinaryI1(t *testing.T) {
	// Verify Parse→Generate preserves protocol fields for AWG 2.0 config
	// with binary I1 blob — this is the exact flow during import+start.
	// Note: Generate() omits Address (wg-quick field, not needed by awg setconf),
	// so we verify the generated output via string matching, not re-parsing.
	origConf := `[Interface]
PrivateKey = aFakePrivateKeyForTesting123456=
Address = 10.0.0.2/32
Jc = 5
Jmin = 10
Jmax = 50
S1 = 63
S2 = 138
S3 = 47
S4 = 19
H1 = 1635672874-1803270462
H2 = 2068218376-2096535092
H3 = 2136583098-2139864411
H4 = 2145353296-2146095616
I1 = <b 0x084481800001000300000000077469636b65747306776964676574>

[Peer]
PublicKey = aFakePublicKeyForTesting7890123=
PresharedKey = aFakePresharedKeyTesting456789=
AllowedIPs = 0.0.0.0/0, ::/0
Endpoint = 203.0.113.1:31582
PersistentKeepalive = 25
`

	// Parse original
	parsed, err := Parse(origConf)
	if err != nil {
		t.Fatalf("Parse(original) error: %v", err)
	}

	// Generate .conf (this is what awg setconf receives)
	generated := Generate(parsed)

	// Protocol fields must survive Parse→Generate
	assertContains(t, generated, "PrivateKey = aFakePrivateKeyForTesting123456=")
	assertContains(t, generated, "I1 = <b 0x084481800001000300000000077469636b65747306776964676574>")
	assertContains(t, generated, "H1 = 1635672874-1803270462")
	assertContains(t, generated, "H4 = 2145353296-2146095616")
	assertContains(t, generated, "S3 = 47")
	assertContains(t, generated, "S4 = 19")
	assertContains(t, generated, "PresharedKey = aFakePresharedKeyTesting456789=")
	// Empty I2-I5 should NOT appear in generated config
	assertNotContains(t, generated, "I2 =")
	assertNotContains(t, generated, "I3 =")
}

// --- Roundtrip tests ---

func TestRoundtrip_ParseThenGenerate(t *testing.T) {
	// Generate creates a "raw" wg conf (no Address/MTU — these are wg-quick
	// fields not understood by awg setconf). So we verify that WireGuard
	// protocol fields survive storage→Generate via string matching.
	tunnel := &storage.AWGTunnel{
		Interface: storage.AWGInterface{
			PrivateKey: "privkey=",
			AWGObfuscation: storage.AWGObfuscation{
				Jc:   4,
				Jmin: 50,
				Jmax: 1000,
				S1:   56,
				S2:   78,
				H1:   "111",
				H2:   "222",
				H3:   "333",
				H4:   "444",
			},
		},
		Peer: storage.AWGPeer{
			PublicKey:           "pubkey=",
			PresharedKey:        "psk=",
			Endpoint:            "server:51820",
			AllowedIPs:          []string{"0.0.0.0/0", "::/0"},
			PersistentKeepalive: 25,
		},
	}

	generated := Generate(tunnel)

	// All protocol fields must appear in generated config
	assertContains(t, generated, "PrivateKey = privkey=")
	assertContains(t, generated, "Jc = 4")
	assertContains(t, generated, "Jmin = 50")
	assertContains(t, generated, "Jmax = 1000")
	assertContains(t, generated, "S1 = 56")
	assertContains(t, generated, "S2 = 78")
	assertContains(t, generated, "H1 = 111")
	assertContains(t, generated, "H2 = 222")
	assertContains(t, generated, "H3 = 333")
	assertContains(t, generated, "H4 = 444")
	assertContains(t, generated, "PublicKey = pubkey=")
	assertContains(t, generated, "PresharedKey = psk=")
	assertContains(t, generated, "Endpoint = server:51820")
	assertContains(t, generated, "AllowedIPs = 0.0.0.0/0, ::/0")
	assertContains(t, generated, "PersistentKeepalive = 25")
	// wg-quick fields must NOT appear
	assertNotContains(t, generated, "Address")
	assertNotContains(t, generated, "MTU")
}

// --- ClassifyAWGVersion tests ---

func TestClassifyAWGVersion_WG(t *testing.T) {
	iface := &storage.AWGInterface{}
	if v := ClassifyAWGVersion(iface); v != "wg" {
		t.Errorf("ClassifyAWGVersion = %q, want %q", v, "wg")
	}
}

func TestClassifyAWGVersion_Nil(t *testing.T) {
	if v := ClassifyAWGVersion(nil); v != "wg" {
		t.Errorf("ClassifyAWGVersion(nil) = %q, want %q", v, "wg")
	}
}

func TestClassifyAWGVersion_AWG10(t *testing.T) {
	iface := &storage.AWGInterface{
		AWGObfuscation: storage.AWGObfuscation{H1: "111", H2: "222", H3: "333", H4: "444"},
	}
	if v := ClassifyAWGVersion(iface); v != "awg1.0" {
		t.Errorf("ClassifyAWGVersion = %q, want %q", v, "awg1.0")
	}
}

func TestClassifyAWGVersion_AWG10_PartialH_IsWG(t *testing.T) {
	// Only some H values set — not enough for AWG 1.0
	iface := &storage.AWGInterface{
		AWGObfuscation: storage.AWGObfuscation{H1: "111", H2: "222"},
	}
	if v := ClassifyAWGVersion(iface); v != "wg" {
		t.Errorf("ClassifyAWGVersion = %q, want %q", v, "wg")
	}
}

func TestClassifyAWGVersion_AWG15(t *testing.T) {
	iface := &storage.AWGInterface{
		AWGObfuscation: storage.AWGObfuscation{H1: "111", H2: "222", H3: "333", H4: "444", I1: "AABB"},
	}
	if v := ClassifyAWGVersion(iface); v != "awg1.5" {
		t.Errorf("ClassifyAWGVersion = %q, want %q", v, "awg1.5")
	}
}

func TestClassifyAWGVersion_AWG20(t *testing.T) {
	iface := &storage.AWGInterface{
		AWGObfuscation: storage.AWGObfuscation{H1: "100-200", H2: "222", H3: "333", H4: "444", I1: "AABB"},
	}
	if v := ClassifyAWGVersion(iface); v != "awg2.0" {
		t.Errorf("ClassifyAWGVersion = %q, want %q", v, "awg2.0")
	}
}

func TestClassifyAWGVersion_AWG20_AnyHRange(t *testing.T) {
	// Even if only H3 is a range, it's still AWG 2.0
	iface := &storage.AWGInterface{
		AWGObfuscation: storage.AWGObfuscation{H1: "111", H2: "222", H3: "10-20", H4: "444"},
	}
	if v := ClassifyAWGVersion(iface); v != "awg2.0" {
		t.Errorf("ClassifyAWGVersion = %q, want %q", v, "awg2.0")
	}
}

func TestClassifyAWGVersion_AWG15_TakesPriorityOverAWG10(t *testing.T) {
	// I1 set + all H values → AWG 1.5 wins over AWG 1.0
	iface := &storage.AWGInterface{
		AWGObfuscation: storage.AWGObfuscation{H1: "111", H2: "222", H3: "333", H4: "444", I1: "sig"},
	}
	if v := ClassifyAWGVersion(iface); v != "awg1.5" {
		t.Errorf("ClassifyAWGVersion = %q, want %q", v, "awg1.5")
	}
}

// --- isRange tests ---

func TestIsRange(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"100-200", true},
		{"0-0", true},
		{"1-999999", true},
		{"", false},
		{"100", false},
		{"abc-def", false},
		{"100-", false},
		{"-200", false},
		{"100-200-300", false},
	}

	for _, tt := range tests {
		if got := isRange(tt.input); got != tt.want {
			t.Errorf("isRange(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

// --- Helpers ---

func assertContains(t *testing.T, s, substr string) {
	t.Helper()
	if !strings.Contains(s, substr) {
		t.Errorf("output does not contain %q:\n%s", substr, s)
	}
}

func assertNotContains(t *testing.T, s, substr string) {
	t.Helper()
	if strings.Contains(s, substr) {
		t.Errorf("output should not contain %q:\n%s", substr, s)
	}
}
