package managed

import (
	"context"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/signature"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/sys/netif"
	"github.com/hoaxisr/awg-manager/internal/tunnel/config"
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

// stubRouterLANIP подменяет определение LAN-адреса роутера: без подмены тест
// зависел бы от сети машины, на которой запущен.
func stubRouterLANIP(t *testing.T, ip string) {
	t.Helper()
	old := netif.RouterLANIP
	netif.RouterLANIP = func(string) string { return ip }
	t.Cleanup(func() { netif.RouterLANIP = old })
}

// Резолвер пира: свой → серверный → LAN-адрес роутера → строки нет вовсе.
// Зашитых публичных адресов в цепочке больше нет (#933): абонент, которому мы
// отдавали 1.1.1.1, резолвил мимо роутера и мимо всех его правил.
func TestGenerateConf_DNSChain(t *testing.T) {
	for _, tc := range []struct {
		name      string
		peerDNS   string
		serverDNS string
		routerIP  string
		want      string // "" — строки DNS быть не должно
	}{
		{"свой у пира", "9.9.9.9", "8.8.4.4", "192.168.1.1", "DNS = 9.9.9.9"},
		{"серверный", "", "8.8.4.4", "192.168.1.1", "DNS = 8.8.4.4"},
		{"LAN-адрес роутера", "", "", "192.168.1.1", "DNS = 192.168.1.1"},
		{"роутер не определился", "", "", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stubRouterLANIP(t, tc.routerIP)
			svc, store, _ := newCreateTestService(t)
			sv := storage.ManagedServer{InterfaceName: "Wireguard1", Address: "10.0.0.1",
				Mask: "255.255.255.0", ListenPort: 51820, Endpoint: "vpn.example.org",
				Policy: "none", DNS: tc.serverDNS,
				Peers: []storage.ManagedPeer{{PublicKey: "PUB", PrivateKey: "PRIV",
					TunnelIP: "10.0.0.2/32", Enabled: true, DNS: tc.peerDNS}}}
			if err := store.AddManagedServer(sv); err != nil {
				t.Fatal(err)
			}
			conf, err := svc.GenerateConf(context.Background(), "Wireguard1", "PUB", "")
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(conf, "1.1.1.1") || strings.Contains(conf, "8.8.8.8") {
				t.Errorf("зашитый публичный резолвер вернулся в файл:\n%s", conf)
			}
			if tc.want == "" {
				if strings.Contains(conf, "DNS =") {
					t.Errorf("резолвера нет — строки быть не должно:\n%s", conf)
				}
				return
			}
			if !strings.Contains(conf, tc.want) {
				t.Errorf("нет %q:\n%s", tc.want, conf)
			}
		})
	}
}

func TestGenerateConf_ClientAllowedIPs(t *testing.T) {
	svc, store, _ := newCreateTestService(t)
	_ = store.AddManagedServer(storage.ManagedServer{InterfaceName: "Wireguard1", Address: "10.0.0.1", Mask: "255.255.255.0", ListenPort: 51820, Endpoint: "vpn.example.org", Policy: "none",
		Peers: []storage.ManagedPeer{
			{PublicKey: "PUB", PrivateKey: "PRIV", TunnelIP: "10.0.0.2/32", Enabled: true},
			{PublicKey: "PUB2", PrivateKey: "PRIV2", TunnelIP: "10.0.0.3/32", Enabled: true, ClientAllowedIPs: "10.0.0.0/24, 192.168.1.0/24"},
		}})
	conf, err := svc.GenerateConf(context.Background(), "Wireguard1", "PUB", "")
	if err != nil || !strings.Contains(conf, "\nAllowedIPs = 0.0.0.0/0, ::/0\n") {
		t.Fatalf("default: %v\n%s", err, conf)
	}
	conf, err = svc.GenerateConf(context.Background(), "Wireguard1", "PUB2", "")
	if err != nil || !strings.Contains(conf, "\nAllowedIPs = 10.0.0.0/24, 192.168.1.0/24\n") || strings.Contains(conf, "0.0.0.0/0") {
		t.Fatalf("custom: %v\n%s", err, conf)
	}
}

// Сервер 3.1 (#688, F681): .conf пира несёт HeaderProtectionKey и
// RandomTrailers сервера, и наш же импорт узнаёт в нём 3.1. Без этих строк
// клиент не подключается — 0 ответов (проба на стенде 20.10).
func TestGenerateConf_AWG31CarriesASC3(t *testing.T) {
	svc, _, getter := newCreateTestService(t)
	svc.supportsASC3 = func() bool { return true }
	server, err := svc.Create(context.Background(), CreateServerRequest{
		Address: "10.55.0.1", Mask: "24", ListenPort: 52055, Endpoint: "vpn.example.org", Mode: ModeAWG31,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	peer, err := svc.AddPeer(context.Background(), server.InterfaceName, AddPeerRequest{Description: "phone"})
	if err != nil {
		t.Fatalf("AddPeer: %v", err)
	}
	conf, err := svc.GenerateConf(context.Background(), server.InterfaceName, peer.PublicKey, "")
	if err != nil {
		t.Fatal(err)
	}
	// QR (F177): потолок 2953 символа при EC=L; ключи фейка короче настоящих
	// на ~120 символов — запас 300 их покрывает.
	if peer.SignatureProfile != "dtls" || len(conf)+300 > 2953 {
		t.Fatalf("профиль %q, длина conf %d — QR не влезет", peer.SignatureProfile, len(conf))
	}
	getter.mu.Lock()
	hp := getter.asc[server.InterfaceName]["header-protection-key"]
	getter.mu.Unlock()
	if hp == "" || !strings.Contains(conf, "\nHeaderProtectionKey = "+hp+"\n") || !strings.Contains(conf, "\nRandomTrailers = on\n") {
		t.Fatalf("conf без параметров 3.1:\n%s", conf)
	}
	if strings.Contains(conf, "RekeyAfterTime") || strings.Contains(conf, "DisableCookies") {
		t.Fatalf("незаданное попало в conf:\n%s", conf)
	}
	parsed, err := config.Parse(conf)
	if err != nil {
		t.Fatalf("наш импорт не принял conf: %v\n%s", err, conf)
	}
	if v := config.ClassifyAWGVersion(&parsed.Interface); v != "awg3.1" {
		t.Fatalf("импорт классифицирует как %s:\n%s", v, conf)
	}
}

// Сервер 2.0: ни одной строки 3.x — конфиг как до #688.
func TestGenerateConf_AWG20HasNoASC3(t *testing.T) {
	svc, _, _ := newCreateTestService(t)
	server, err := svc.Create(context.Background(), CreateServerRequest{
		Address: "10.56.0.1", Mask: "24", ListenPort: 52056, Endpoint: "vpn.example.org",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	peer, err := svc.AddPeer(context.Background(), server.InterfaceName, AddPeerRequest{Description: "phone"})
	if err != nil {
		t.Fatalf("AddPeer: %v", err)
	}
	conf, err := svc.GenerateConf(context.Background(), server.InterfaceName, peer.PublicKey, "")
	if err != nil {
		t.Fatal(err)
	}
	if peer.SignatureProfile != signature.DefaultProfile {
		t.Fatalf("профиль пира сервера 2.0: %q", peer.SignatureProfile)
	}
	if !strings.Contains(conf, "Jc = ") || strings.Contains(conf, "HeaderProtectionKey") || strings.Contains(conf, "RandomTrailers") {
		t.Fatalf("conf сервера 2.0:\n%s", conf)
	}
}
