package managed

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// Ревью F469/F476, п.4: статистика managed-сервера берёт пиров из
// WGServers.Get (кэш на 30 с), а живые поля — из PeerStore. Без наложения
// цифры на странице серверов стояли до 30 с после изменения, а с редкой
// подсказкой (F469) — до следующего трафика.
func TestGetStats_OverlaysLivePeers(t *testing.T) {
	store := storage.NewSettingsStore(t.TempDir())
	if _, err := store.Load(); err != nil {
		t.Fatal(err)
	}
	if err := store.AddManagedServer(storage.ManagedServer{InterfaceName: "Wireguard0"}); err != nil {
		t.Fatal(err)
	}
	fg := query.NewFakeGetter()
	const peer = `"public-key":"PK1","txbytes":5,"last-handshake":30,"online":true,"enabled":true`
	// Кэш items (POST show interface) — старые цифры; PeerStore (GET) — живые.
	fg.SetPostInterface("Wireguard0", `{"show":{"interface":{"id":"Wireguard0","type":"Wireguard","state":"up","wireguard":{"peer":[{"rxbytes":10,`+peer+`}]}}}}`)
	fg.SetJSON("/show/interface/Wireguard0", `{"id":"Wireguard0","type":"Wireguard","state":"up","wireguard":{"peer":[{"rxbytes":99,`+peer+`}]}}`)
	fg.SetJSON("/show/interface/", `{}`)
	fg.SetJSON("/show/rc/interface/Wireguard0", `{}`) // обогащение Get без rc — ошибка (F510)
	queries := query.NewQueries(query.Deps{Getter: fg, Logger: query.NopLogger()})
	svc := New(&fakePoster{}, nil, queries, nil, store, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)

	stats, err := svc.GetStats(context.Background(), "Wireguard0")
	if err != nil {
		t.Fatalf("GetStats: %v", err)
	}
	if len(stats.Peers) != 1 || stats.Peers[0].RxBytes != 99 {
		t.Fatalf("peers = %+v, ждали rx=99 из PeerStore, а не 10 из кэша", stats.Peers)
	}
}
