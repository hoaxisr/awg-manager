package service

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
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

// Handler после svc.Update fail-closed, поэтому адрес target'а в запись кладёт
// сам сервис: host-route уже переставлен, и потерять адрес нельзя.
func TestPersistObfuscatorTargetIP(t *testing.T) {
	dir := t.TempDir()
	store := storage.NewAWGTunnelStoreWithLockDir(dir, filepath.Join(dir, "locks"))
	if err := store.Create(&storage.AWGTunnel{ID: "awg20", ResolvedEndpointIP: "198.51.100.1"}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	s := &ServiceImpl{store: store}

	s.persistObfuscatorTargetIP("awg20", "203.0.113.5")
	if saved, _ := store.Get("awg20"); saved.ResolvedEndpointIP != "203.0.113.5" {
		t.Fatalf("target IP не записан: %q", saved.ResolvedEndpointIP)
	}
	// Пустой адрес не имеет права затирать записанный, а несуществующий
	// туннель — ронять вызывающего.
	s.persistObfuscatorTargetIP("awg20", "")
	s.persistObfuscatorTargetIP("awg99", "203.0.113.5")
	if saved, _ := store.Get("awg20"); saved.ResolvedEndpointIP != "203.0.113.5" {
		t.Fatalf("target IP затёрт: %q", saved.ResolvedEndpointIP)
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

const obfImportConf = "[Interface]\nPrivateKey = cHJpdmF0ZWtleXByaXZhdGVrZXlwcml2YXRla2V5MDA=\nAddress = 10.8.0.2/32\n\n[Peer]\nPublicKey = cHVibGlja2V5cHVibGlja2V5cHVibGlja2V5cHViMDA=\nAllowedIPs = 0.0.0.0/0\nEndpoint = 1.2.3.4:51820\n"

// Ручной ClusterM с AWG-параметрами отвергается на импорте, как и на правке
// (F119 5.7); Phobos этим гейтом не трогается.
func TestImport_ClusterMRejectsAWGParams(t *testing.T) {
	dir := t.TempDir()
	s := &ServiceImpl{store: storage.NewAWGTunnelStoreWithLockDir(dir, filepath.Join(dir, "locks"))}
	conf := strings.Replace(obfImportConf, "Address = 10.8.0.2/32\n", "Address = 10.8.0.2/32\nJc = 4\nJmin = 40\nJmax = 70\nH1 = 1\nH2 = 2\nH3 = 3\nH4 = 4\n", 1)
	o := &storage.Obfuscator{Target: "vpn.example.com:51824", Key: "k", Masking: "STUN"}

	cm := *o
	cm.Flavor = storage.ObfuscatorFlavorClusterM
	_, err := s.Import(context.Background(), conf, "cm", "", ImportLink{Obfuscator: &cm})
	if err == nil || !strings.Contains(err.Error(), "несовместимы") {
		t.Fatalf("ClusterM с AWG-параметрами: err = %v", err)
	}
	ph := *o
	ph.Flavor = storage.ObfuscatorFlavorPhobos
	if _, err := s.Import(context.Background(), conf, "ph", "", ImportLink{Obfuscator: &ph}); err != nil && strings.Contains(err.Error(), "несовместимы") {
		t.Fatalf("Phobos попал под гейт ClusterM: %v", err)
	}
}

// Два параллельных импорта обфусцированных туннелей получают разные порты:
// выбор порта и запись идут под одним importIDMu (F119 4.1).
func TestImport_ObfuscatedConcurrentDistinctPorts(t *testing.T) {
	op, _, _ := nwgOnOracle(t)
	dir := t.TempDir()
	s := &ServiceImpl{state: NewMockStateManager(), nwgOperator: op,
		store: storage.NewAWGTunnelStoreWithLockDir(dir, filepath.Join(dir, "locks"))}
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			o := &storage.Obfuscator{Flavor: storage.ObfuscatorFlavorPhobos, Target: "vpn.example.com:51824", Key: "k", Masking: "STUN"}
			_, errs[i] = s.Import(context.Background(), obfImportConf, fmt.Sprintf("t%d", i), "", ImportLink{Obfuscator: o})
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatalf("import: %v", err)
		}
	}
	all, err := s.store.List()
	if err != nil || len(all) != 2 || all[0].Obfuscator == nil || all[1].Obfuscator == nil {
		t.Fatalf("записи: %+v (%v)", all, err)
	}
	if all[0].Obfuscator.LocalPort == all[1].Obfuscator.LocalPort {
		t.Fatalf("оба туннеля на порту %d", all[0].Obfuscator.LocalPort)
	}
}
