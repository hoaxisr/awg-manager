package wdtt

import "testing"

func TestDecodeImport_WdttColon(t *testing.T) {
	link := "wdtt://1.2.3.4:56000:56001:0:secret:hash1,hash2#MyServer"
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

func TestApplyImport(t *testing.T) {
	cfg := DefaultClientConfig()
	p := ImportPayload{
		Peer:     "1.1.1.1:56000",
		Password: "p",
		VKHashes: []string{"h1"},
		Workers:  36,
		Listen:   "127.0.0.1:9101",
	}
	out := ApplyImport(cfg, p)
	if out.Peer != p.Peer || out.Password != p.Password || out.VKHashes != "h1" || out.Workers != 36 {
		t.Fatalf("cfg=%+v", out)
	}
}
