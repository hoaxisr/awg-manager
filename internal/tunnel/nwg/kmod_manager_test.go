package nwg

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// buildKmodConfigResolved must carry the awg3.1 params from storage: the
// base64 HeaderProtectionKey becomes 64-hex, RandomTrailers passes through, and
// S1-S4 are clamped to the header-protection minimum (12 = ChaCha20 nonce).
func TestBuildKmodConfig_AWG31(t *testing.T) {
	keyBytes := bytes.Repeat([]byte{0x01}, 32)
	stored := &storage.AWGTunnel{
		Interface: storage.AWGInterface{
			AWGObfuscation: storage.AWGObfuscation{
				S1: 8, S2: 40, S3: 40, S4: 40, // S1 below the minimum
				HeaderProtectionKey: base64.StdEncoding.EncodeToString(keyBytes),
				RandomTrailers:      true,
			},
		},
	}

	cfg, err := buildKmodConfigResolved(stored, "1.2.3.4", 51820, "")
	if err != nil {
		t.Fatalf("buildKmodConfigResolved: %v", err)
	}
	if cfg.HeaderProtectionKeyHex != hex.EncodeToString(keyBytes) {
		t.Errorf("HP key hex = %q", cfg.HeaderProtectionKeyHex)
	}
	if !cfg.RandomTrailers {
		t.Error("RandomTrailers not carried through")
	}
	if cfg.S1 != 12 {
		t.Errorf("S1 not clamped to 12: got %d", cfg.S1)
	}
	if cfg.S2 != 40 {
		t.Errorf("S2 should be untouched (>=12): got %d", cfg.S2)
	}
}

func TestPubKeyToHex(t *testing.T) {
	key := "YWJjZGVmZ2hpamtsbW5vcHFyc3R1dnd4eXoxMjM0NTY="
	hex := pubKeyToHex(key)
	if len(hex) != 64 {
		t.Errorf("pubKeyToHex: expected 64 hex chars, got %d", len(hex))
	}
	if got := pubKeyToHex("invalid"); got != "" {
		t.Errorf("pubKeyToHex(invalid) = %q, want empty", got)
	}
}

func TestBuildProcLine(t *testing.T) {
	cfg := KmodConfig{
		EndpointIP:   "1.2.3.4",
		EndpointPort: 51820,
		H1:           "1", H2: "2", H3: "3", H4: "4",
		S1: 10, S2: 20, S3: 0, S4: 0,
		Jc: 5, Jmin: 50, Jmax: 1000,
	}
	line := buildProcLine(cfg)
	if line == "" {
		t.Error("buildProcLine returned empty")
	}
	expected := "1.2.3.4:51820"
	if len(line) < len(expected) || line[:len(expected)] != expected {
		t.Errorf("buildProcLine prefix = %q, want prefix %q", line[:20], expected)
	}
}

func TestBuildProcLine_AWG31(t *testing.T) {
	cfg := KmodConfig{
		EndpointIP:   "1.2.3.4",
		EndpointPort: 51820,
		H1:           "1", H2: "2", H3: "3", H4: "4",
		S1: 12, S2: 12, S3: 12, S4: 12,
		HeaderProtectionKeyHex: "01020304050607080910111213141516" +
			"01020304050607080910111213141516",
		RandomTrailers: true,
	}
	line := buildProcLine(cfg)
	if !strings.Contains(line, " HP_KEY="+cfg.HeaderProtectionKeyHex) {
		t.Errorf("missing HP_KEY in %q", line)
	}
	if !strings.Contains(line, " RT=1") {
		t.Errorf("missing RT=1 in %q", line)
	}
}

func TestBuildProcLine_NoAWG31WhenUnset(t *testing.T) {
	cfg := KmodConfig{
		EndpointIP: "1.2.3.4", EndpointPort: 51820,
		H1: "1", H2: "2", H3: "3", H4: "4",
	}
	line := buildProcLine(cfg)
	if strings.Contains(line, "HP_KEY=") || strings.Contains(line, "RT=") {
		t.Errorf("awg3.1 tokens leaked into a non-awg3.1 line: %q", line)
	}
}

func TestEndpointKey(t *testing.T) {
	if got := endpointKey("1.2.3.4", 51820); got != "1.2.3.4:51820" {
		t.Errorf("endpointKey v4 = %q, want 1.2.3.4:51820", got)
	}
	if got := endpointKey("2001:db8::1", 51820); got != "[2001:db8::1]:51820" {
		t.Errorf("endpointKey v6 = %q, want [2001:db8::1]:51820", got)
	}
	// Hostnames stay unbracketed — only literal-v6 (contains ':') is wrapped.
	if got := endpointKey("vpn.example.com", 443); got != "vpn.example.com:443" {
		t.Errorf("endpointKey host = %q, want vpn.example.com:443", got)
	}
}

func TestBuildProcLine_IPv6(t *testing.T) {
	cfg := KmodConfig{
		EndpointIP:   "2001:db8::1",
		EndpointPort: 51820,
		H1:           "1", H2: "2", H3: "3", H4: "4",
	}
	line := buildProcLine(cfg)
	const wantPrefix = "[2001:db8::1]:51820 "
	if !strings.HasPrefix(line, wantPrefix) {
		t.Errorf("buildProcLine v6 = %q, want prefix %q", line, wantPrefix)
	}
}

func TestCountProxySlotsList(t *testing.T) {
	data := `
(proxy slots)
144.31.251.248:32663 listen=127.0.0.1:52046 rx=10 tx=20

bad-line
1.2.3.4:443 listen=127.0.0.1:51820 rx=0 tx=0
`

	if got := countProxySlotsList(data); got != 2 {
		t.Fatalf("countProxySlotsList() = %d, want 2", got)
	}
}

func TestCountProxySlotsListEmpty(t *testing.T) {
	for _, data := range []string{"", "\n", "(empty)\n", "1.2.3.4:443 rx=0 tx=0"} {
		if got := countProxySlotsList(data); got != 0 {
			t.Fatalf("countProxySlotsList(%q) = %d, want 0", data, got)
		}
	}
}

func TestHasSlotListeningInList(t *testing.T) {
	const list = `46.149.74.35:443 listen=127.0.0.1:51958 rx=10 tx=20
1.2.3.4:51820 listen=127.0.0.1:40001 rx=0 tx=0`
	if !hasSlotListeningInList(list, 51958) {
		t.Error("want true for listen port 51958")
	}
	if !hasSlotListeningInList(list, 40001) {
		t.Error("want true for listen port 40001")
	}
	if hasSlotListeningInList(list, 99999) {
		t.Error("want false for absent listen port 99999")
	}
	if hasSlotListeningInList("", 51958) {
		t.Error("want false for empty list")
	}
}

// --- IPv6 list rows: "[v6]:port ..." as printed by kmod >= 1.3.0 ----------

const mixedFamilyList = "[2001:db8::1]:51820 listen=127.0.0.1:50001 rx=1 tx=2 rx_pkt=1 tx_pkt=1\n" +
	"1.2.3.4:443 listen=127.0.0.1:50002 rx=0 tx=0 rx_pkt=0 tx_pkt=0\n"

func TestCountProxySlotsList_IPv6Rows(t *testing.T) {
	if got := countProxySlotsList(mixedFamilyList); got != 2 {
		t.Fatalf("countProxySlotsList() = %d, want 2 (v6 row must count)", got)
	}
}

func TestHasSlotListeningInList_IPv6Rows(t *testing.T) {
	if !hasSlotListeningInList(mixedFamilyList, 50001) {
		t.Error("want true for listen port 50001 (v6 row)")
	}
	if !hasSlotListeningInList(mixedFamilyList, 50002) {
		t.Error("want true for listen port 50002 (v4 row)")
	}
}

func TestReadListenPortLocked_MixedFamilies(t *testing.T) {
	km, _ := newKmodManagerForTest()
	km.procReadFn = func(path string) ([]byte, error) {
		return []byte(mixedFamilyList), nil
	}

	km.mu.Lock()
	defer km.mu.Unlock()

	port, err := km.readListenPortLocked("2001:db8::1", 51820)
	if err != nil {
		t.Fatalf("readListenPortLocked v6: %v", err)
	}
	if port != 50001 {
		t.Errorf("v6 listen port = %d, want 50001", port)
	}

	port, err = km.readListenPortLocked("1.2.3.4", 443)
	if err != nil {
		t.Fatalf("readListenPortLocked v4: %v", err)
	}
	if port != 50002 {
		t.Errorf("v4 listen port = %d, want 50002", port)
	}

	if _, err := km.readListenPortLocked("2001:db8::2", 51820); err == nil {
		t.Error("want error for absent v6 endpoint")
	}
}

func TestReadListenPortLocked_EmbeddedIPv4Forms(t *testing.T) {
	// The kernel's %pI6c renders addresses with an embedded IPv4 tail
	// (ISATAP ::5efe:x.x.x.x and friends) in dotted-quad form, while Go's
	// net.IP.String() prints hex groups ("::5efe:c000:201"). A string
	// prefix match never fires → orphan live slot. The row must be found
	// by PARSED address comparison.
	km, _ := newKmodManagerForTest()
	km.procReadFn = func(path string) ([]byte, error) {
		return []byte("[::5efe:192.0.2.1]:51820 listen=127.0.0.1:50003 rx=0 tx=0 rx_pkt=0 tx_pkt=0\n"), nil
	}

	km.mu.Lock()
	defer km.mu.Unlock()

	// Go's canonical spelling of the same address.
	port, err := km.readListenPortLocked("::5efe:c000:201", 51820)
	if err != nil {
		t.Fatalf("readListenPortLocked ISATAP: %v", err)
	}
	if port != 50003 {
		t.Errorf("ISATAP listen port = %d, want 50003", port)
	}

	// Same address, WRONG port — must not match.
	if _, err := km.readListenPortLocked("::5efe:c000:201", 443); err == nil {
		t.Error("want error for same address with different port")
	}
	// Different address, same port — must not match.
	if _, err := km.readListenPortLocked("::5efe:c000:202", 51820); err == nil {
		t.Error("want error for different address")
	}
}

// --- IPv6 endpoints are gated on kmod >= kmodVersionIPv6 -------------------

func TestAddTunnel_IPv6RequiresKmod130(t *testing.T) {
	km, stub := newKmodManagerForTest() // stub reports version 1.1.10
	cfg := defaultCfg()
	cfg.EndpointIP = "2001:db8::1"

	_, err := km.AddTunnel("tunnel-v6-old-kmod", cfg)
	if err == nil {
		t.Fatal("AddTunnel with IPv6 endpoint on kmod 1.1.10 must fail")
	}
	if !strings.Contains(err.Error(), kmodVersionIPv6) {
		t.Errorf("error should name required version %s; got: %v", kmodVersionIPv6, err)
	}
	if got := stub.countWritesTo("/proc/awg_proxy/add"); got != 0 {
		t.Errorf("no /proc/add write may happen on gate failure; got %d", got)
	}
}

func TestAddTunnel_IPv6BracketedLineOnNewKmod(t *testing.T) {
	km, stub := newKmodManagerForTest()
	stub.version = "1.3.0"
	cfg := defaultCfg()
	cfg.EndpointIP = "2001:db8::1"

	res, err := km.AddTunnel("tunnel-v6", cfg)
	if err != nil {
		t.Fatalf("AddTunnel v6: %v", err)
	}
	if res.ListenPort == 0 {
		t.Error("listen port must be read back from the v6 list row")
	}

	var addBody string
	for _, w := range stub.writes {
		if w.path == "/proc/awg_proxy/add" {
			addBody = w.body
		}
	}
	if !strings.HasPrefix(addBody, "[2001:db8::1]:5060 ") {
		t.Errorf("add line must use the bracketed form; got %q", addBody)
	}
}
