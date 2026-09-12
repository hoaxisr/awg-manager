package obfuscator

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

const phobosConf = `[Interface]
PrivateKey = MBrnZoTdyT/LR4XpB7tElSxyVTQdXFw0tvVJOMSL/GI=
Address = 10.8.0.4/32, fdcc:ad94:bacf:61a4::cafe:4/128
MTU = 1420

[Peer]
PublicKey = g/G4y2XkTY5mPLMYYXXCarvyxUSHUzM1vpIYRHwwFT4=
AllowedIPs = 0.0.0.0/0, ::/0
Endpoint = 127.0.0.1:13255

[instance]
source-if = 127.0.0.1
source-lport = 13255
target = 203.0.113.136:51824
key = fixture-key-0000000
masking = STUN
obfuscate-bytes = 16
max-dummy = 45
idle-timeout = 300
verbose = INFO
media-ssrc = 1
`

func TestParseInstance_Phobos(t *testing.T) {
	o, unknown, present, err := ParseInstance(phobosConf)
	if err != nil || !present {
		t.Fatalf("err=%v present=%v", err, present)
	}
	want := storage.Obfuscator{Flavor: "phobos", Target: "203.0.113.136:51824", Key: "fixture-key-0000000",
		Masking: "STUN", MaxDummy: 45, IdleTimeout: 300, ObfuscateBytes: 16}
	if *o != want {
		t.Fatalf("got %+v want %+v", *o, want)
	}
	// source-if/source-lport/verbose — наши, не «неизвестные»; media-ssrc — неизвестный.
	if strings.Join(unknown, ",") != "media-ssrc" {
		t.Fatalf("unknown = %v", unknown)
	}
}

func TestParseInstance_Absent(t *testing.T) {
	o, _, present, err := ParseInstance("[Interface]\nPrivateKey = x\n[Peer]\nPublicKey = y\n")
	if err != nil || present || o != nil {
		t.Fatalf("o=%v present=%v err=%v", o, present, err)
	}
}

func TestParseInstance_BadNumberRejected(t *testing.T) {
	if _, _, _, err := ParseInstance("[instance]\ntarget = a:1\nkey = k\nmax-dummy = abc\n"); err == nil {
		t.Fatal("non-numeric max-dummy must be an error, not silent 0")
	}
}

func TestParseInstance_Socks5Rejected(t *testing.T) {
	_, _, _, err := ParseInstance("[instance]\nmode = socks5\ntarget = a:1\nkey = k\n")
	if err == nil || !strings.Contains(err.Error(), "socks5") {
		t.Fatalf("want socks5 error, got %v", err)
	}
}

func TestStripInstance(t *testing.T) {
	out := StripInstance(phobosConf)
	if strings.Contains(out, "[instance]") || strings.Contains(out, "target =") {
		t.Fatalf("instance not stripped:\n%s", out)
	}
	if !strings.Contains(out, "[Peer]") || !strings.Contains(out, "Endpoint = 127.0.0.1:13255") {
		t.Fatalf("peer damaged:\n%s", out)
	}
}

func TestRenderInstance_RoundTrip(t *testing.T) {
	o := &storage.Obfuscator{Flavor: "phobos", Target: "h:1", Key: "k", Masking: "MEDIA", MaxDummy: 4, IdleTimeout: 60, ObfuscateBytes: 16, LocalPort: 39001}
	back, _, present, err := ParseInstance("[Peer]\nPublicKey = y\n" + RenderInstance(o))
	if err != nil || !present {
		t.Fatal(err)
	}
	o.LocalPort = 0 // LocalPort в [instance] не едет (source-lport — наш)
	if *back != *o {
		t.Fatalf("got %+v want %+v", *back, *o)
	}
	if strings.Contains(RenderInstance(&storage.Obfuscator{Flavor: "clusterm", Target: "h:1", Key: "k", Masking: "STUN", ObfuscateBytes: 16}), "obfuscate-bytes") {
		t.Fatal("clusterm must not render obfuscate-bytes")
	}
}

func TestNormalizeNoneValues(t *testing.T) {
	in := "[Interface]\nMTU = none\nDNS = None\nAddress = 10.0.0.1/32\n[Peer]\nPresharedKey = none\nPersistentKeepalive = none\n[instance]\nidle-timeout = none\nkey = k\n"
	out := NormalizeNoneValues(in)
	for _, bad := range []string{"MTU = none", "DNS = None", "PresharedKey = none", "PersistentKeepalive = none", "idle-timeout = none"} {
		if strings.Contains(out, bad) {
			t.Fatalf("%q survived:\n%s", bad, out)
		}
	}
	if !strings.Contains(out, "Address = 10.0.0.1/32") || !strings.Contains(out, "key = k") {
		t.Fatalf("real values lost:\n%s", out)
	}
}

func TestDecodePhobosLink(t *testing.T) {
	payload := base64.RawURLEncoding.EncodeToString([]byte(phobosConf))
	conf, name, err := DecodePhobosLink("phobos://" + payload + "#Mobil%20phone")
	if err != nil || conf != phobosConf || name != "Mobil phone" {
		t.Fatalf("conf-eq=%v name=%q err=%v", conf == phobosConf, name, err)
	}
	if _, _, err := DecodePhobosLink("phobos://v2.abc#x"); err == nil {
		t.Fatal("v2 prefix must be rejected")
	}
	if !IsPhobosLink("  phobos://x") || IsPhobosLink("vpn://x") {
		t.Fatal("IsPhobosLink")
	}
}
