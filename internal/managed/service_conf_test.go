package managed

import (
	"context"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// seedASC кладёт в фейковый NDMS числовые ASC-параметры интерфейса. Без них
// signature.WriteASCConf выходит рано (Jc == 0) — и ни Jc, ни сигнатуры в .conf нет.
func seedASC(t *testing.T, g *stateAwareGetter, iface string) {
	t.Helper()
	g.mu.Lock()
	defer g.mu.Unlock()
	g.asc[iface] = map[string]string{
		"jc": "3", "jmin": "10", "jmax": "50", "s1": "0", "s2": "0",
		"h1": "1000001", "h2": "1000002", "h3": "1000003", "h4": "1000004",
		"s3": "0", "s4": "0",
	}
}

func TestGenerateConf_UsesPeerSignature(t *testing.T) {
	svc, store, getter := newCreateTestService(t)
	sv := storage.ManagedServer{InterfaceName: "Wireguard1", Address: "10.0.0.1", Mask: "255.255.255.0", ListenPort: 51820, Endpoint: "vpn.example.org", Policy: "none",
		Peers: []storage.ManagedPeer{
			{PublicKey: "PUB", PrivateKey: "PRIV", PresharedKey: "PSK", TunnelIP: "10.0.0.2/32", Enabled: true,
				I1: "<b 0x0102>", I2: "<r 7>", SignatureProfile: "dns"},
			{PublicKey: "PUB2", PrivateKey: "PRIV2", PresharedKey: "PSK2", TunnelIP: "10.0.0.3/32", Enabled: true},
		}}
	if err := store.AddManagedServer(sv); err != nil {
		t.Fatal(err)
	}
	seedASC(t, getter, "Wireguard1")

	conf, err := svc.GenerateConf(context.Background(), "Wireguard1", "PUB", "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(conf, "\nI1 = <b 0x0102>\n") || !strings.Contains(conf, "\nI2 = <r 7>\n") || strings.Contains(conf, "I3 =") {
		t.Fatalf("conf:\n%s", conf)
	}
	conf2, err := svc.GenerateConf(context.Background(), "Wireguard1", "PUB2", "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(conf2, "I1 =") {
		t.Fatalf("peer without signature must not get I-lines:\n%s", conf2)
	}
	if !strings.Contains(conf2, "Jc = 3") {
		t.Fatal("numeric ASC params still come from NDMS")
	}
}

func TestGenerateConf_NoASC_SkipsSignature(t *testing.T) {
	svc, store, _ := newCreateTestService(t)
	_ = store.AddManagedServer(storage.ManagedServer{InterfaceName: "Wireguard1", Address: "10.0.0.1", Mask: "255.255.255.0", ListenPort: 51820, Endpoint: "vpn.example.org", Policy: "none",
		Peers: []storage.ManagedPeer{{PublicKey: "PUB", PrivateKey: "PRIV", PresharedKey: "PSK", TunnelIP: "10.0.0.2/32", Enabled: true, I1: "<b 0x01>", SignatureProfile: "dns"}}})
	// ASC у сервера не задан (stateAwareGetter.asc пуст → Jc == 0)
	conf, err := svc.GenerateConf(context.Background(), "Wireguard1", "PUB", "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(conf, "I1 =") || strings.Contains(conf, "Jc =") {
		t.Fatalf("plain WG server must not emit AWG params:\n%s", conf)
	}
}
