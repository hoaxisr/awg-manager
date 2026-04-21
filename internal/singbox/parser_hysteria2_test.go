package singbox

import (
	"encoding/json"
	"testing"
)

func TestParseHysteria2_Full(t *testing.T) {
	link := "hysteria2://secret-pass@fi.example.com:8443?sni=mask.example.com&insecure=1#Finland"
	got, err := parseHysteria2(link)
	if err != nil {
		t.Fatal(err)
	}
	if got.Tag != "Finland" || got.Server != "fi.example.com" || got.Port != 8443 {
		t.Errorf("basic: %+v", got)
	}
	var raw map[string]any
	_ = json.Unmarshal(got.Outbound, &raw)
	if raw["type"] != "hysteria2" || raw["password"] != "secret-pass" {
		t.Error("type/password")
	}
	if raw["hop_interval"] != "10s" {
		t.Errorf("hop_interval: %v", raw["hop_interval"])
	}
	tls := raw["tls"].(map[string]any)
	if tls["server_name"] != "mask.example.com" || tls["insecure"] != true {
		t.Error("tls")
	}
	alpn, _ := tls["alpn"].([]any)
	if len(alpn) != 1 || alpn[0] != "h3" {
		t.Errorf("alpn: %v", tls["alpn"])
	}
}

func TestParseHysteria2_Hy2Scheme(t *testing.T) {
	got, err := parseHysteria2("hy2://pw@h.tld:443#x")
	if err != nil {
		t.Fatal(err)
	}
	if got.Protocol != "hysteria2" {
		t.Errorf("protocol=%s", got.Protocol)
	}
}

func TestParseHysteria2_UserPassJoined(t *testing.T) {
	link := "hy2://chapaev:CvsjKMZfc97vbxubjCbGUGzDLAYlze2p@144.31.127.72:1937?insecure=1&sni=www.ebay.com#IPv4"
	got, err := parseHysteria2(link)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	_ = json.Unmarshal(got.Outbound, &raw)
	if raw["password"] != "chapaev:CvsjKMZfc97vbxubjCbGUGzDLAYlze2p" {
		t.Errorf("password: got %q", raw["password"])
	}
}

func TestParseHysteria2_Obfs(t *testing.T) {
	link := "hy2://pw@h.tld:443?obfs=salamander&obfs-password=s3cret#x"
	got, err := parseHysteria2(link)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	_ = json.Unmarshal(got.Outbound, &raw)
	obfs, ok := raw["obfs"].(map[string]any)
	if !ok {
		t.Fatal("obfs block missing")
	}
	if obfs["type"] != "salamander" || obfs["password"] != "s3cret" {
		t.Errorf("obfs: %+v", obfs)
	}
}

func TestParseHysteria2_PinSHA256Dropped(t *testing.T) {
	link := "hy2://chapaev:CvsjKMZfc97vbxubjCbGUGzDLAYlze2p@144.31.127.72:1937?insecure=1&sni=www.ebay.com&obfs=salamander&obfs-password=4t4DFTeQX5hYA3apmIiokKGxP1vfyG&pinSHA256=D8:DD:1E:E4:05:F9:43:69:0A:98:93:AD:13:0B:9B:3D:EC:1F:6B:15:18:E1:7F:44:E7:35:87:BC:3D:AA:D3:3B#IPv4"
	got, err := parseHysteria2(link)
	if err != nil {
		t.Fatalf("pinSHA256 must be dropped, not rejected: %v", err)
	}
	var raw map[string]any
	_ = json.Unmarshal(got.Outbound, &raw)
	if _, has := raw["pinSHA256"]; has {
		t.Error("pinSHA256 leaked into outbound")
	}
	tls := raw["tls"].(map[string]any)
	for _, k := range []string{"certificate_public_key_sha256", "certificate", "certificate_path"} {
		if _, has := tls[k]; has {
			t.Errorf("tls must not contain %q derived from pinSHA256", k)
		}
	}
}

func TestParseHysteria2_Missing(t *testing.T) {
	cases := []string{
		"vless://pw@host:443",       // wrong scheme
		"hysteria2://host:443",      // missing password (no user info)
		"hysteria2://@host:443",     // empty password
		"hysteria2://pw@:443",       // missing host
		"hysteria2://pw@host",       // missing port
		"hysteria2://pw@host:abc",   // non-numeric port
		"hysteria2://pw@host:0",     // out of range
		"hysteria2://pw@host:99999", // out of range
	}
	for _, c := range cases {
		if _, err := parseHysteria2(c); err == nil {
			t.Errorf("expected error for %q", c)
		}
	}
}
