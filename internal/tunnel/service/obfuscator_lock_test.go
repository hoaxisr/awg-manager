package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
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

// fakeRelay — минимальный ObfuscatorRunner: релей нам нужен живым, чтобы Start
// дошёл до резолва target'а и положил адрес в трекер оператора.
type fakeRelay struct{ alive map[string]bool }

func (f *fakeRelay) Start(_ context.Context, id string, _ *storage.Obfuscator) error {
	if f.alive == nil {
		f.alive = map[string]bool{}
	}
	f.alive[id] = true
	return nil
}
func (f *fakeRelay) Stop(id string) error { delete(f.alive, id); return nil }
func (f *fakeRelay) Alive(id string) bool { return f.alive[id] }

// Замена конфигурации у работающего обфусцированного туннеля обязана оставить
// в записи адрес target'а: по нему после рестарта демона снимается host-route.
// Раньше адрес возвращал второй проход SyncObfuscator — теперь он берётся у
// оператора, и без этой проверки правку можно было бы молча выбросить.
func TestReplaceConfig_PersistsObfuscatorTargetIP(t *testing.T) {
	op := nwgOperatorOnStub(t)
	op.SetObfuscator(&fakeRelay{})
	dir := t.TempDir()
	store := storage.NewAWGTunnelStoreWithLockDir(dir, filepath.Join(dir, "locks"))
	s := &ServiceImpl{store: store, state: NewMockStateManager(), nwgOperator: op}

	if err := store.Create(&storage.AWGTunnel{
		ID: "awg20", Name: "obf", Backend: "nativewg", Enabled: true, NWGIndex: 0,
		Interface: storage.AWGInterface{Address: "10.9.0.2/32", MTU: 1420, PrivateKey: "k"},
		Peer:      storage.AWGPeer{PublicKey: "PUB", Endpoint: "127.0.0.1:39000"},
		Obfuscator: &storage.Obfuscator{
			Flavor: storage.ObfuscatorFlavorPhobos, Target: "203.0.113.9:51820",
			Key: "k", Masking: "STUN", MaxDummy: 4, LocalPort: 39000,
		},
	}); err != nil {
		t.Fatalf("подготовка: %v", err)
	}

	if err := s.ReplaceConfig(context.Background(), "awg20", sampleConf, ""); err != nil {
		t.Fatalf("ReplaceConfig: %v", err)
	}

	saved, err := store.Get("awg20")
	if err != nil {
		t.Fatal(err)
	}
	if saved.ResolvedEndpointIP != "203.0.113.9" {
		t.Fatalf("адрес target'а не сохранён: %q", saved.ResolvedEndpointIP)
	}
}
