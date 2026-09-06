package service

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/obfuscator"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

func TestPrepareObfuscatorImport(t *testing.T) {
	parsed := &storage.AWGTunnel{Backend: "kernel", Peer: storage.AWGPeer{Endpoint: "1.2.3.4:51820"}, ResolvedEndpointIP: "1.2.3.4"}
	o := &storage.Obfuscator{Flavor: "phobos", Target: "vpn.example.com:51824", Key: "k", Masking: "STUN", MaxDummy: 4}
	taken := func(p int) bool { return p == obfuscator.PortMin } // первый порт «занят» другим туннелем
	if err := prepareObfuscatorImport(parsed, o, taken); err != nil {
		t.Fatal(err)
	}
	if parsed.Backend != "nativewg" || parsed.Obfuscator == nil {
		t.Fatalf("%+v", parsed)
	}
	if parsed.Obfuscator.LocalPort == obfuscator.PortMin || parsed.Obfuscator.LocalPort == 0 {
		t.Fatalf("port %d", parsed.Obfuscator.LocalPort)
	}
	if parsed.Peer.Endpoint != fmt.Sprintf("127.0.0.1:%d", parsed.Obfuscator.LocalPort) || parsed.ResolvedEndpointIP != "" {
		t.Fatalf("endpoint %q resolved %q", parsed.Peer.Endpoint, parsed.ResolvedEndpointIP)
	}
	if o.LocalPort != 0 {
		t.Fatal("input must not be mutated")
	}
	bad := &storage.Obfuscator{Flavor: "phobos", Target: "x", Key: "k", Masking: "STUN"}
	if err := prepareObfuscatorImport(&storage.AWGTunnel{}, bad, taken); err == nil {
		t.Fatal("invalid obfuscator must be rejected")
	}
}

func TestObfuscatorPortTaken(t *testing.T) {
	dir := t.TempDir()
	store := storage.NewAWGTunnelStoreWithLockDir(dir, filepath.Join(dir, "locks"))
	if err := store.Create(&storage.AWGTunnel{ID: "awg20", Obfuscator: &storage.Obfuscator{LocalPort: 39007}}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	s := &ServiceImpl{store: store}
	if !s.obfuscatorPortTaken(39007) || s.obfuscatorPortTaken(39008) {
		t.Fatal("obfuscatorPortTaken")
	}
}
