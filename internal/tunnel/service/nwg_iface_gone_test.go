package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/ndms/command"
	"github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/ndms/transport"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
	"github.com/hoaxisr/awg-manager/internal/tunnel/nwg"
)

// Правка nativewg, чей WireguardN снят в NDMS: явная ErrInterfaceGone до
// любой команды (F546) — handler fail-closed не сохранит карточку, а команды
// по снятому имени создали бы интерфейс заново.
func TestApplyDiffNWG_InterfaceGone_NoCommands(t *testing.T) {
	var batches atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		batches.Add(1)
		_, _ = w.Write([]byte(`[{}]`))
	}))
	t.Cleanup(srv.Close)
	f := query.NewFakeNDMS() // Wireguard0 нет
	q := query.NewQueries(query.Deps{Getter: f, Logger: query.NopLogger(), IsOS5: func() bool { return true }})
	sc := command.NewSaveCoordinator(f, nopPublisher{}, 500*time.Millisecond, 5*time.Second, 0, nil)
	sc.SetSaveTimings(command.SaveEventCap, 0, command.SaveAfterRemoval) // без шины событий
	cmds := command.NewCommands(command.Deps{Poster: f, Save: sc, Queries: q, IsOS5: func() bool { return true }})
	tr := transport.NewWithURL(srv.URL, transport.NewSemaphore(2))
	op := nwg.NewOperator(q, cmds, tr, nil)
	t.Cleanup(func() { op.Close(); tr.Close() })
	s := &ServiceImpl{state: NewMockStateManager(), nwgOperator: op}

	old := &storage.AWGTunnel{ID: "awg20", Backend: "nativewg",
		Interface: storage.AWGInterface{Address: "10.0.0.1/32", MTU: 1420, DNS: "1.1.1.1"}}
	upd := *old
	upd.Interface.MTU = 1400
	upd.Interface.DNS = "9.9.9.9"

	err := s.applyDiffNWG(context.Background(), old, &upd)
	if !errors.Is(err, tunnel.ErrInterfaceGone) {
		t.Fatalf("want ErrInterfaceGone, got %v", err)
	}
	if batches.Load() != 0 || len(f.Posts) != 0 || f.Phantoms != 0 || f.E != 0 {
		t.Fatalf("batches=%d posts=%v phantoms=%d E=%d", batches.Load(), f.Posts, f.Phantoms, f.E)
	}
	if got := f.ListCalls(); got != 1 {
		t.Fatalf("list reads = %d, want 1 per edit", got)
	}
}

// Замена .conf у nativewg при непрочитанном списке интерфейсов: отказ ДО
// сохранения записи (F546, решение 4) — запись прежняя, в NDMS ничего. Иначе
// запись уже новая, а роутер со старым пиром, и всё это под видом успеха.
func TestReplaceConfigNWG_ListFails_RecordUnchanged(t *testing.T) {
	var posts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			posts.Add(1)
		}
		_, _ = w.Write([]byte(`[{}]`))
	}))
	t.Cleanup(srv.Close)
	f := query.NewFakeNDMS(ndms.Interface{ID: "Wireguard0", Type: "Wireguard"})
	q := query.NewQueries(query.Deps{Getter: f, Logger: query.NopLogger(), IsOS5: func() bool { return true }})
	sc := command.NewSaveCoordinator(f, nopPublisher{}, 500*time.Millisecond, 5*time.Second, 0, nil)
	sc.SetSaveTimings(command.SaveEventCap, 0, command.SaveAfterRemoval) // без шины событий
	cmds := command.NewCommands(command.Deps{Poster: f, Save: sc, Queries: q, IsOS5: func() bool { return true }})
	tr := transport.NewWithURL(srv.URL, transport.NewSemaphore(2))
	op := nwg.NewOperator(q, cmds, tr, nil)
	t.Cleanup(func() { op.Close(); tr.Close() })
	s, _ := serviceWithStore(t)
	s.nwgOperator = op

	orig := &storage.AWGTunnel{ID: "awg20", Name: "nwg", Backend: "nativewg",
		Interface: storage.AWGInterface{Address: "10.0.0.1/32", MTU: 1420, DNS: "1.1.1.1"},
		Peer:      storage.AWGPeer{PublicKey: "old", Endpoint: "198.51.100.1:51820"}}
	if err := s.store.Create(orig); err != nil {
		t.Fatal(err)
	}
	before, _ := s.store.Get("awg20")

	f.FailList(errors.New("rci down"))
	if err := s.ReplaceConfig(context.Background(), "awg20", sampleConf, "renamed", ReplaceOptions{}); err == nil {
		t.Fatal("ReplaceConfig при непрочитанном списке: want error, got nil")
	}
	after, _ := s.store.Get("awg20")
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("запись изменена при отказе:\nbefore=%+v\nafter=%+v", before, after)
	}
	if posts.Load() != 0 || len(f.Posts) != 0 || f.Phantoms != 0 {
		t.Fatalf("posts=%d fake=%v phantoms=%d", posts.Load(), f.Posts, f.Phantoms)
	}
}
