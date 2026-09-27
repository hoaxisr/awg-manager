package managed

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/ndms/command"
	"github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/peersubnet"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/sys/netif"
)

// foreignKey — чужой пир системного сервера Wireguard0 (заведён в веб-морде роутера).
const foreignKey = "FOREIGN0000000000000000000000000000000000000="

// newPeerSubnetTestService — Service над FakeGetter (статический NDMS) и
// recordingPoster; Commands построены над тем же poster'ом, поэтому allow-ips и
// маршруты видны в posts. Managed-сервер Wireguard1 уже в сторе.
// Роутер: системный Wireguard0 10.9.0.0/24 с чужим пиром (172.16.5.0/24 в
// allow-ips), managed Wireguard1 10.66.66.0/24, LAN Home 192.168.1.0/24,
// Guest 10.1.30.0/24; клиентский Wireguard2 (не сервер) с пиром 0.0.0.0/0.
func newPeerSubnetTestService(t *testing.T, rcRoutes string) (*Service, *storage.SettingsStore, *recordingPoster, *query.FakeGetter) {
	t.Helper()
	fg := query.NewFakeGetter()
	fg.SetJSON("/show/interface/", `{
		"Wireguard0":{"id":"Wireguard0","type":"Wireguard","description":"Wireguard VPN Server","state":"up","link":"up","address":"10.9.0.1","mask":"255.255.255.0","wireguard":{"peer":[{"public-key":"`+foreignKey+`","comment":"office"}]}},
		"Wireguard1":{"id":"Wireguard1","type":"Wireguard","description":"AWGM WG Server","state":"up","link":"up","address":"10.66.66.1","mask":"255.255.255.0"},
		"Wireguard2":{"id":"Wireguard2","type":"Wireguard","description":"VPN client","state":"up","link":"up","address":"10.8.1.2","mask":"255.255.255.0"},
		"Bridge0":{"id":"Bridge0","type":"Bridge","description":"Home","address":"192.168.1.1","mask":"255.255.255.0"},
		"Bridge1":{"id":"Bridge1","type":"Bridge","description":"Guest","address":"10.1.30.1","mask":"255.255.255.0"}}`)
	fg.SetJSON("/show/rc/interface/Wireguard0", `{"wireguard":{"peer":[{"key":"`+foreignKey+`","comment":"office","allow-ips":[{"address":"10.9.0.2","mask":"255.255.255.255"},{"address":"172.16.5.0","mask":"255.255.255.0"}]}]}}`)
	fg.SetJSON("/show/rc/interface/Wireguard1", `{}`)
	// Клиентский туннель (NativeWG): пир с 0.0.0.0/0 — не сервер, занятых не даёт.
	fg.SetJSON("/show/rc/interface/Wireguard2", `{"wireguard":{"peer":[{"key":"VPNPEER=","allow-ips":[{"address":"0.0.0.0","mask":"0"}]}]}}`)
	fg.SetJSON("/show/rc/ip/route", rcRoutes)
	fg.SetJSON("/show/running-config", `{"message":[]}`)
	queries := query.NewQueries(query.Deps{Getter: fg, Logger: query.NopLogger()})
	store := storage.NewSettingsStore(t.TempDir())
	if _, err := store.Load(); err != nil {
		t.Fatal(err)
	}
	poster := &recordingPoster{}
	sc := command.NewSaveCoordinator(poster, nil, time.Hour, time.Hour, 0, nil)
	cmds := command.NewCommands(command.Deps{Poster: poster, Save: sc, Queries: queries})
	svc := New(poster, sc, queries, cmds, store, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	svc.keyGen = &fakeKeyGen{}
	if err := store.MarkServerInterface("Wireguard0"); err != nil {
		t.Fatal(err)
	}
	if err := store.AddManagedServer(storage.ManagedServer{InterfaceName: "Wireguard1", Address: "10.66.66.1", Mask: "255.255.255.0", ListenPort: 51821, Policy: "none"}); err != nil {
		t.Fatal(err)
	}
	return svc, store, poster, fg
}

func labels(occ []peersubnet.Occupied) map[string]string {
	out := map[string]string{}
	for _, o := range occ {
		out[o.Net.String()] = o.Label
	}
	return out
}

func TestOccupiedSubnets_CollectsAllSources(t *testing.T) {
	svc, store, _, _ := newPeerSubnetTestService(t, `[]`)
	_ = store.UpdateManagedServer("Wireguard1", func(sv *storage.ManagedServer) error {
		sv.Peers = append(sv.Peers, storage.ManagedPeer{PublicKey: "PEER1", Description: "branch", RemoteSubnets: []string{"192.168.50.0/24"}})
		return nil
	})
	_ = store.SetServerPeerSecret("Wireguard0", "SYS1=", storage.ServerPeerSecret{PrivateKey: "p", Description: "home", RemoteSubnets: []string{"192.168.60.0/24"}})

	occ, err := svc.OccupiedSubnets(context.Background(), PeerRef{})
	if err != nil {
		t.Fatal(err)
	}
	got := labels(occ)
	want := map[string]string{
		"10.9.0.0/24":     "интерфейс Wireguard VPN Server",
		"10.66.66.0/24":   "интерфейс AWGM WG Server",
		"192.168.1.0/24":  "интерфейс Home",
		"10.1.30.0/24":    "интерфейс Guest",
		"172.16.5.0/24":   "пир «office» сервера Wireguard0",
		"192.168.50.0/24": "пир «branch» сервера Wireguard1",
		"192.168.60.0/24": "пир «home» сервера Wireguard0",
	}
	for cidr, label := range want {
		if got[cidr] != label {
			t.Errorf("%s: label %q, want %q (all: %v)", cidr, got[cidr], label, got)
		}
	}
	for cidr, label := range got {
		if strings.Contains(label, "Wireguard2") || cidr == "0.0.0.0/0" {
			t.Fatalf("пир клиентского туннеля в занятых: %s %q", cidr, label)
		}
	}
	// /32 пира внутри подсети своего сервера покрыт подсетью — в списке его нет.
	if _, ok := got["10.9.0.2/32"]; ok {
		t.Fatal("собственный /32 пира попал в занятые")
	}
	// Правимый пир свои сети не занимает.
	occ, _ = svc.OccupiedSubnets(context.Background(), PeerRef{Iface: "Wireguard0", PubKey: foreignKey})
	if _, ok := labels(occ)["172.16.5.0/24"]; ok {
		t.Fatal("сети правимого пира учтены как занятые")
	}
	occ, _ = svc.OccupiedSubnets(context.Background(), PeerRef{Iface: "Wireguard1", PubKey: "PEER1"})
	if _, ok := labels(occ)["192.168.50.0/24"]; ok {
		t.Fatal("хранимые сети правимого пира учтены как занятые")
	}
}

// Review Focus 5: роутер не отвечает — отказ, а не пустой список занятых.
func TestOccupiedSubnets_RouterUnreachableIsError(t *testing.T) {
	svc, _, _, fg := newPeerSubnetTestService(t, `[]`)
	fg.SetError("/show/interface/", errors.New("rci down"))
	if _, err := svc.OccupiedSubnets(context.Background(), PeerRef{}); err == nil {
		t.Fatal("ожидали ошибку")
	}
}

// allow-ips пиров чужого сервера не прочитались — отказ: список серверов
// (WGServers.List) такой сбой глотает, и занятых там было бы меньше.
func TestOccupiedSubnets_PeerConfigUnreachableIsError(t *testing.T) {
	svc, _, _, fg := newPeerSubnetTestService(t, `[]`)
	fg.SetError("/show/rc/interface/Wireguard0", errors.New("rci down"))
	if _, err := svc.OccupiedSubnets(context.Background(), PeerRef{}); err == nil {
		t.Fatal("ожидали ошибку")
	}
}

func TestPeerPresets_LANSegmentsAndDNSChain(t *testing.T) {
	svc, store, _, _ := newPeerSubnetTestService(t, `[]`)
	old := netif.RouterLANIP
	netif.RouterLANIP = func(string) string { return "192.168.1.1" }
	t.Cleanup(func() { netif.RouterLANIP = old })
	ctx := context.Background()

	p, err := svc.PeerPresets(ctx, "Wireguard1", "")
	if err != nil {
		t.Fatal(err)
	}
	if p.RouterOnly != "10.66.66.0/24, 10.1.30.0/24, 192.168.1.0/24" {
		t.Fatalf("все бриджи: %q", p.RouterOnly)
	}
	if !strings.Contains(p.ExceptRouter, "192.168.1.1/32") || !strings.HasSuffix(p.ExceptRouter, ", ::/0") {
		t.Fatalf("резолвер по умолчанию не защищён: %q", p.ExceptRouter)
	}
	_ = store.UpdateManagedServer("Wireguard1", func(sv *storage.ManagedServer) error {
		sv.LANSegments = []string{"Bridge0"}
		sv.DNS = "9.9.9.9"
		return nil
	})
	p, _ = svc.PeerPresets(ctx, "Wireguard1", "")
	if p.RouterOnly != "10.66.66.0/24, 192.168.1.0/24" || strings.Contains(p.ExceptRouter, "/32") {
		t.Fatalf("LANSegments/DNS сервера: %+v", p)
	}
	p, _ = svc.PeerPresets(ctx, "Wireguard1", "192.168.1.1, 8.8.8.8")
	if !strings.Contains(p.ExceptRouter, "192.168.1.1/32") || strings.Contains(p.ExceptRouter, "8.8.8.8/32") {
		t.Fatalf("dns из формы: %q", p.ExceptRouter)
	}
	if _, err := svc.PeerPresets(ctx, "Wireguard1", "not-an-ip"); !errors.Is(err, ErrInvalidPeerDNS) {
		t.Fatalf("err = %v", err)
	}
}

// Удаление интерфейса уносит его маршруты: после мутации через rciPost кэш
// маршрутов обязан перечитаться, иначе владение сверялось бы по ушедшим записям.
func TestRCIPost_InvalidatesStaticRoutes(t *testing.T) {
	svc, _, _, fg := newPeerSubnetTestService(t, `[{"network":"192.168.50.0","mask":"255.255.255.0","interface":"Wireguard1","comment":"awgm-peer:PEER1"}]`)
	ctx := context.Background()
	if got, err := svc.queries.StaticRoutes.List(ctx); err != nil || len(got) != 1 {
		t.Fatalf("прогрев: %v %v", got, err)
	}
	fg.SetJSON("/show/rc/ip/route", `[]`)
	if err := svc.rciPost(ctx, map[string]any{"interface": map[string]any{"name": "Wireguard1", "no": true}}); err != nil {
		t.Fatal(err)
	}
	if got, _ := svc.queries.StaticRoutes.List(ctx); len(got) != 0 {
		t.Fatalf("кэш маршрутов не сброшен: %v", got)
	}
}

// Отказ отката Apply уходит в журнал приложения текстом Rollback; прочие
// ошибки (без отката) журнал не трогают.
func TestLogRollback(t *testing.T) {
	svc, _, _, _ := newPeerSubnetTestService(t, `[]`)
	spy := &recAppLog{}
	svc.appLog = logging.NewScopedLogger(spy, logging.GroupServer, logging.SubManaged)

	svc.logRollback("peer-subnets", "Wireguard1", errors.New("plain"))
	if len(spy.entries) != 0 {
		t.Fatalf("журнал без отката: %v", spy.entries)
	}
	err := fmt.Errorf("apply: %w", &peersubnet.RollbackError{Cause: errors.New("add route"), Rollback: errors.New("no such net in peer")})
	svc.logRollback("peer-subnets", "Wireguard1", err)
	want := "warn|peer-subnets|Wireguard1|откат сетей за клиентом не завершён: no such net in peer"
	if len(spy.entries) != 1 || spy.entries[0] != want {
		t.Fatalf("журнал = %v", spy.entries)
	}
}
