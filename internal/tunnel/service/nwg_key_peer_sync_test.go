package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/ndms/command"
	"github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/ndms/transport"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel/nwg"
)

// F650: на смене пути (ASC ↔ awg_proxy) туннель перезапускается, а Start ни
// приватного ключа, ни пира не шлёт — их досылает syncKeyAndPeer.
func TestSyncKeyAndPeer_SendsOnlyChanged(t *testing.T) {
	for _, tc := range []struct {
		name               string
		newKey, newAllowed string
		wantKey, wantPeer  bool
	}{
		{"ничего не менялось", "", "", false, false},
		{"сменился ключ", "NEWPRIV000000000000000000000000000000000000=", "", true, false},
		{"сменились AllowedIPs", "", "10.0.0.0/8", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			var bodies []string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				b, _ := io.ReadAll(r.Body)
				mu.Lock()
				bodies = append(bodies, string(b))
				mu.Unlock()
				_, _ = w.Write([]byte(`[{}]`))
			}))
			t.Cleanup(srv.Close)
			f := query.NewFakeNDMS(ndms.Interface{ID: "Wireguard0", Type: "Wireguard"})
			q := query.NewQueries(query.Deps{Getter: f, Logger: query.NopLogger(), IsOS5: func() bool { return true }})
			sc := command.NewSaveCoordinator(f, nopPublisher{}, time.Hour, time.Hour, 0, nil)
			sc.SetSaveTimings(command.SaveEventCap, 0, command.SaveAfterRemoval)
			cmds := command.NewCommands(command.Deps{Poster: f, Save: sc, Queries: q, IsOS5: func() bool { return true }})
			tr := transport.NewWithURL(srv.URL, transport.NewSemaphore(2))
			op := nwg.NewOperator(q, cmds, tr, nil)
			t.Cleanup(func() { op.Close(); tr.Close() })
			s := &ServiceImpl{state: NewMockStateManager(), nwgOperator: op}

			old := &storage.AWGTunnel{ID: "awg20", Backend: "nativewg",
				Interface: storage.AWGInterface{PrivateKey: "OLDPRIV000000000000000000000000000000000000=", Address: "10.0.0.1/32"},
				Peer: storage.AWGPeer{PublicKey: "peerkey000000000000000000000000000000000000=",
					Endpoint: "192.0.2.7:51820", AllowedIPs: []string{"0.0.0.0/0"}}}
			upd := *old
			if tc.newKey != "" {
				upd.Interface.PrivateKey = tc.newKey
			}
			if tc.newAllowed != "" {
				upd.Peer.AllowedIPs = []string{tc.newAllowed}
			}

			if err := s.syncKeyAndPeer(context.Background(), old, &upd); err != nil {
				t.Fatalf("syncKeyAndPeer: %v", err)
			}
			mu.Lock()
			joined := strings.Join(bodies, "\n") + strings.Join(f.Posts, "\n")
			mu.Unlock()
			if got := strings.Contains(joined, `"private-key"`); got != tc.wantKey {
				t.Errorf("private-key отправлен = %v, ждали %v; тела: %s", got, tc.wantKey, joined)
			}
			if got := strings.Contains(joined, `"allow-ips"`); got != tc.wantPeer {
				t.Errorf("пир отправлен = %v, ждали %v; тела: %s", got, tc.wantPeer, joined)
			}
		})
	}
}

// F650, корень: Update остановленного NativeWG-туннеля досылает изменённые
// ключ и пир — Start их не шлёт; без интерфейса в NDMS правка проходит без
// команд.
func TestUpdateStoppedNWG_SyncsKeyAndPeer(t *testing.T) {
	for _, tc := range []struct {
		name     string
		ifaces   []ndms.Interface
		wantSent bool
	}{
		{"интерфейс есть", []ndms.Interface{{ID: "Wireguard0", Type: "Wireguard"}}, true},
		{"интерфейса нет", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			var bodies []string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				b, _ := io.ReadAll(r.Body)
				mu.Lock()
				bodies = append(bodies, string(b))
				mu.Unlock()
				_, _ = w.Write([]byte(`[{}]`)) // GetState: записи нет → туннель не работает
			}))
			t.Cleanup(srv.Close)
			f := query.NewFakeNDMS(tc.ifaces...)
			q := query.NewQueries(query.Deps{Getter: f, Logger: query.NopLogger(), IsOS5: func() bool { return true }})
			sc := command.NewSaveCoordinator(f, nopPublisher{}, time.Hour, time.Hour, 0, nil)
			sc.SetSaveTimings(command.SaveEventCap, 0, command.SaveAfterRemoval)
			cmds := command.NewCommands(command.Deps{Poster: f, Save: sc, Queries: q, IsOS5: func() bool { return true }})
			tr := transport.NewWithURL(srv.URL, transport.NewSemaphore(2))
			op := nwg.NewOperator(q, cmds, tr, nil)
			t.Cleanup(func() { op.Close(); tr.Close() })
			s, _ := serviceWithStore(t)
			s.nwgOperator = op

			old := &storage.AWGTunnel{ID: "awg20", Name: "nwg", Backend: "nativewg",
				Interface: storage.AWGInterface{PrivateKey: "OLDPRIV000000000000000000000000000000000000=", Address: "10.0.0.1/32", MTU: 1420},
				Peer: storage.AWGPeer{PublicKey: "peerkey000000000000000000000000000000000000=",
					Endpoint: "192.0.2.7:51820", AllowedIPs: []string{"0.0.0.0/0"}}}
			upd := *old
			upd.Interface.PrivateKey = "NEWPRIV000000000000000000000000000000000000="

			if err := s.Update(context.Background(), old, &upd); err != nil {
				t.Fatalf("Update: %v", err)
			}
			mu.Lock()
			joined := strings.Join(bodies, "\n") + strings.Join(f.Posts, "\n")
			mu.Unlock()
			if got := strings.Contains(joined, `"private-key"`); got != tc.wantSent {
				t.Fatalf("private-key отправлен = %v, ждали %v; тела: %s", got, tc.wantSent, joined)
			}
		})
	}
}
