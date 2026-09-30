package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

const liveServerJSON = `{"id":"Wireguard0","type":"Wireguard","description":"Wireguard VPN Server","state":"up","link":"up","address":"10.9.0.1","mask":"255.255.255.0","wireguard":{"peer":[{"public-key":"PK1","rxbytes":%d,"txbytes":5,"last-handshake":30,"online":true,"enabled":true}]}}`

func liveServerEntry(rx int) string {
	return fmt.Sprintf(liveServerJSON, rx)
}

// F476: /api/servers/all берёт список серверов из кэша (TTL 5 мин), а живые
// поля пиров — из PeerStore. Без наложения счётчики на странице серверов
// стояли до 5 минут, хотя поллер метрик видел свежие.
func TestServersGetAll_OverlaysLivePeers(t *testing.T) {
	fg := query.NewFakeGetter()
	fg.SetJSON("/show/interface/", `{"Wireguard0":`+liveServerEntry(10)+`}`)
	// Обогащение списка серверов читает rc каждого: без него List — ошибка (F510).
	fg.SetRC("Wireguard0", `{}`)
	queries := query.NewQueries(query.Deps{Getter: fg, Logger: query.NopLogger()})
	store := storage.NewSettingsStore(t.TempDir())
	if _, err := store.Load(); err != nil {
		t.Fatal(err)
	}
	h := NewServersHandler(queries, store, nil, nil)

	rx := func() int64 {
		w := httptest.NewRecorder()
		h.GetAll(w, httptest.NewRequest(http.MethodGet, "/api/servers/all", nil))
		var resp struct {
			Data struct {
				Servers []struct {
					Peers []struct {
						RxBytes int64 `json:"rxBytes"`
					} `json:"peers"`
				} `json:"servers"`
			} `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil || len(resp.Data.Servers) != 1 || len(resp.Data.Servers[0].Peers) != 1 {
			t.Fatalf("ответ %s: %v", w.Body.String(), err)
		}
		return resp.Data.Servers[0].Peers[0].RxBytes
	}
	if got := rx(); got != 10 {
		t.Fatalf("первый ответ rx=%d, want 10", got)
	}
	// Роутер насчитал трафик; список серверов в кэше прежний, PeerStore
	// освежился (его держит поллер, TTL 8 с — здесь сбрасываем явно; пиры —
	// из снимка списка, он не старше 2 с — здесь метим его грязным).
	fg.SetJSON("/show/interface/", `{"Wireguard0":`+liveServerEntry(99)+`}`)
	queries.Interfaces.Invalidate("Wireguard0")
	queries.Peers.Invalidate("Wireguard0")
	if got := rx(); got != 99 {
		t.Fatalf("rx=%d, want 99 — счётчики взяты из кэша списка, а не живые", got)
	}
}
