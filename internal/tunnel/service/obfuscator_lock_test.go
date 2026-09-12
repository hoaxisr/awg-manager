package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms/command"
	"github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/ndms/transport"
	"github.com/hoaxisr/awg-manager/internal/orchestrator"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
	"github.com/hoaxisr/awg-manager/internal/tunnel/nwg"
)

type nopPublisher struct{}

func (nopPublisher) Publish(string, any) {}

// nwgOperatorOnStub — настоящий оператор, но весь RCI уходит в заглушку:
// нам важен порядок взятия замка, а не ответы роутера.
func nwgOperatorOnStub(t *testing.T) *nwg.OperatorNativeWG {
	t.Helper()
	// Интерфейс отвечает «поднят»: иначе Update решит, что синхронизировать
	// нечего, и до ветки под замком не дойдёт.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"show":{"interface":{"id":"Wireguard0","link":"up","state":"up",
			"summary":{"layer":{"conf":"running","link":"running"}},
			"wireguard":{"status":"up","peer":[{"online":true}]}}}}`))
	}))
	t.Cleanup(srv.Close)
	tr := transport.NewWithURL(srv.URL, transport.NewSemaphore(2))
	q := query.NewQueries(query.Deps{Getter: tr, Logger: query.NopLogger(), IsOS5: func() bool { return true }})
	sc := command.NewSaveCoordinator(tr, nopPublisher{}, 500*time.Millisecond, 5*time.Second, 0, nil)
	cmds := command.NewCommands(command.Deps{Poster: tr, Save: sc, Queries: q, IsOS5: func() bool { return true }})
	op := nwg.NewOperator(q, cmds, tr, nil)
	t.Cleanup(func() { op.Close(); tr.Close() })
	return op
}

// Правка живого nativewg-туннеля идёт под тем же per-tunnel замком, что и
// действия оркестратора: иначе она переплетается с WAN-up по тому же туннелю,
// и команды в NDMS наезжают друг на друга.
func TestUpdate_NativeWGDiffTakesTunnelLock(t *testing.T) {
	orch := orchestrator.New(nil, nil, nil, nil, nil, nil)
	s := &ServiceImpl{state: NewMockStateManager(), nwgOperator: nwgOperatorOnStub(t)}
	s.SetOrchestrator(orch)

	// Замок занят кем-то другим — держим его из соседней горутины, как это
	// делал бы WAN-up; контекст правки отменён, чтобы не ждать таймаут.
	held, release := make(chan struct{}), make(chan struct{})
	go func() {
		_ = orch.WithTunnelLock(context.Background(), "awg20", "wan-up", func() error {
			close(held)
			<-release
			return nil
		})
	}()
	<-held
	defer close(release)
	// Короткий дедлайн вместо отмены: чтение состояния по RCI должно успеть
	// (иначе Update решит, что туннель не запущен, и до замка не дойдёт), а
	// ожидание занятого замка — упереться в дедлайн, не в tunnelLockTimeout.
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	old := &storage.AWGTunnel{
		ID: "awg20", Backend: "nativewg",
		Interface: storage.AWGInterface{Address: "10.0.0.1/32", MTU: 1420},
	}
	updated := &storage.AWGTunnel{
		ID: "awg20", Backend: "nativewg",
		Interface:  storage.AWGInterface{Address: "10.0.0.1/32", MTU: 1420},
		Obfuscator: &storage.Obfuscator{Flavor: storage.ObfuscatorFlavorPhobos, Target: "203.0.113.9:51820", Key: "k", LocalPort: 39000},
	}

	err := s.Update(ctx, old, updated)
	if err == nil {
		t.Fatal("правка живого туннеля обязана споткнуться о занятый замок")
	}
	if !errors.Is(err, tunnel.ErrOperationInProgress) {
		t.Fatalf("ждали отказ по замку, получили: %v", err)
	}
}
