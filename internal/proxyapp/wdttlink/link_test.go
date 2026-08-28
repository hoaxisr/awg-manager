package wdttlink

import (
	"net"
	"strings"
	"testing"
)

func TestValidateSubURL_RejectsInternal(t *testing.T) {
	for _, u := range []string{
		"http://localhost:79/x",
		"http://127.0.0.1/x",
		"http://[::1]/x",
		"http://169.254.1.1/x",
		"http://0.0.0.0/x",
	} {
		if err := validateSubURL(u); err == nil {
			t.Errorf("expected rejection for %s", u)
		}
	}
}

func TestValidateSubURL_RejectsBadScheme(t *testing.T) {
	if err := validateSubURL("ftp://example.com/x"); err == nil {
		t.Error("expected scheme rejection")
	}
	if err := validateSubURL("http:///x"); err == nil {
		t.Error("expected hostless rejection")
	}
}

func TestValidateSubURL_AcceptsPublic(t *testing.T) {
	orig := lookupIP
	defer func() { lookupIP = orig }()
	lookupIP = func(string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("93.184.216.34")}, nil
	}
	if err := validateSubURL("https://example.com/sub"); err != nil {
		t.Fatalf("expected public host accepted, got %v", err)
	}
}

func TestValidateSubURL_RejectsRebindViaSeam(t *testing.T) {
	orig := lookupIP
	defer func() { lookupIP = orig }()
	lookupIP = func(string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("127.0.0.1")}, nil
	}
	if err := validateSubURL("https://evil.example.com/sub"); err == nil {
		t.Error("expected rejection for host resolving to loopback")
	}
}

func TestBlockInternalDial(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:443", "[::1]:443", "169.254.1.1:80"} {
		if err := blockInternalDial("tcp", addr, nil); err == nil {
			t.Errorf("expected rejection for %s", addr)
		}
	}
	for _, addr := range []string{"8.8.8.8:443", "93.184.216.34:443"} {
		if err := blockInternalDial("tcp", addr, nil); err != nil {
			t.Errorf("expected accept for %s, got %v", addr, err)
		}
	}
}

func TestNormalizeSubURL_KeepsQuery(t *testing.T) {
	in := "https://sub.example.com/wdtt.json?token=abc123"
	if got := normalizeSubURL(in); got != in {
		t.Fatalf("query stripped: %q", got)
	}
	if got := normalizeSubURL("  ftp://x/y  "); got != "" {
		t.Fatalf("expected empty for non-http, got %q", got)
	}
}

func TestEncodeLink_ColonFormat(t *testing.T) {
	link, err := EncodeLink("1.2.3.4:56000", 56001, "secret", []string{"hash1", "hash2"}, "MyServer")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(link, ":9000:secret:") {
		t.Fatalf("link must include client listen port 9000, got %q", link)
	}
	got, err := DecodeImport(link)
	if err != nil {
		t.Fatal(err)
	}
	if got.Peer != "1.2.3.4:56000" || got.Password != "secret" || len(got.VKHashes) != 2 {
		t.Fatalf("roundtrip failed: %+v", got)
	}
	if got.Listen != "127.0.0.1:9000" {
		t.Fatalf("listen=%q want 127.0.0.1:9000", got.Listen)
	}
	if got.Name != "MyServer" {
		t.Fatalf("name=%q", got.Name)
	}
}

func TestEncodeQwdttLink_Port9000(t *testing.T) {
	link, err := EncodeQwdttLink("1.2.3.4:56001", "secret", []string{"h1"}, "Srv", 0, 18, "")
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeImport(link)
	if err != nil {
		t.Fatal(err)
	}
	if got.Listen != "127.0.0.1:9000" {
		t.Fatalf("listen=%q", got.Listen)
	}
}

func TestDecodeImport_WdttColon(t *testing.T) {
	link := "wdtt://1.2.3.4:56000:56001:9000:secret:hash1,hash2#MyServer"
	got, err := DecodeImport(link)
	if err != nil {
		t.Fatal(err)
	}
	if got.Peer != "1.2.3.4:56000" {
		t.Fatalf("peer=%q", got.Peer)
	}
	if got.Password != "secret" {
		t.Fatalf("password=%q", got.Password)
	}
	if len(got.VKHashes) != 2 || got.VKHashes[0] != "hash1" {
		t.Fatalf("hashes=%v", got.VKHashes)
	}
	if got.Name != "MyServer" {
		t.Fatalf("name=%q", got.Name)
	}
}

func TestDecodeImport_Qwdtt(t *testing.T) {
	link := "qwdtt://config?name=Home&peer=203.0.113.1:56000&hashes=abc&workers=24&port=9100&pass=pwd"
	got, err := DecodeImport(link)
	if err != nil {
		t.Fatal(err)
	}
	if got.Peer != "203.0.113.1:56000" {
		t.Fatalf("peer=%q", got.Peer)
	}
	if got.Password != "pwd" {
		t.Fatalf("password=%q", got.Password)
	}
	if got.Workers != 24 {
		t.Fatalf("workers=%d", got.Workers)
	}
	if got.Listen != "127.0.0.1:9100" {
		t.Fatalf("listen=%q", got.Listen)
	}
}

func TestDecodeImport_QwdttPeerWithoutPort(t *testing.T) {
	link := "qwdtt://config?peer=10.0.0.1&pass=x&hashes=h"
	got, err := DecodeImport(link)
	if err != nil {
		t.Fatal(err)
	}
	if got.Peer != "10.0.0.1:56000" {
		t.Fatalf("peer=%q", got.Peer)
	}
}

func TestDecodeImport_QwdttJSONFile(t *testing.T) {
	const body = `{
  "name": "WL RUS",
  "peer": "77.90.61.238",
  "hashes": "https://vk.com/call/join/m0mwRXzYPZNMvTI0kx6jPnVc8HJOUxV3izOqu_0w3zU",
  "workers": 18,
  "port": 9000,
  "password": "vana8a6d"
}`
	got, err := DecodeImport(body)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "WL RUS" {
		t.Fatalf("name=%q", got.Name)
	}
	if got.Peer != "77.90.61.238:56000" {
		t.Fatalf("peer=%q", got.Peer)
	}
	if got.Password != "vana8a6d" {
		t.Fatalf("password=%q", got.Password)
	}
	if got.Listen != "127.0.0.1:9000" {
		t.Fatalf("listen=%q", got.Listen)
	}
	if len(got.VKHashes) != 1 || got.VKHashes[0] != "m0mwRXzYPZNMvTI0kx6jPnVc8HJOUxV3izOqu_0w3zU" {
		t.Fatalf("hashes=%v", got.VKHashes)
	}
}
