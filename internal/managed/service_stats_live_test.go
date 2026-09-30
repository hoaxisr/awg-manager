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
	// Интерфейс есть и в списке, как на роутере: без него кэш счёл бы его
	// отсутствующим и пиров не спрашивал бы (F546).
	// Runtime сервера и пиры — из снимка списка (F546).
	fg.SetJSON("/show/interface/", `{"Wireguard0":{"id":"Wireguard0","type":"Wireguard","state":"up","wireguard":{"peer":[{"rxbytes":10,`+peer+`}]}}}`)
	fg.SetJSON("/show/rc/interface/Wireguard0", `{}`) // обогащение Get без rc — ошибка (F510)
	queries := query.NewQueries(query.Deps{Getter: fg, Logger: query.NopLogger()})
	svc := New(&fakePoster{}, nil, queries, nil, store, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	// Кэш items заполнен старыми цифрами; к моменту GetStats роутер отвечает
	// живыми — их и должен принести PeerStore.
	if _, err := queries.WGServers.Get(context.Background(), "Wireguard0"); err != nil {
		t.Fatal(err)
	}
	// Снимок не старше 2 с — метим его грязным: следующий читатель берёт
	// свежий список с живыми цифрами.
	fg.SetJSON("/show/interface/", `{"Wireguard0":{"id":"Wireguard0","type":"Wireguard","state":"up","wireguard":{"peer":[{"rxbytes":99,`+peer+`}]}}}`)
	queries.Interfaces.Invalidate("Wireguard0")

	stats, err := svc.GetStats(context.Background(), "Wireguard0")
	if err != nil {
		t.Fatalf("GetStats: %v", err)
	}
	if len(stats.Peers) != 1 || stats.Peers[0].RxBytes != 99 {
		t.Fatalf("peers = %+v, ждали rx=99 из PeerStore, а не 10 из кэша", stats.Peers)
	}
}
