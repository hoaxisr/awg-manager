//go:build linux

package instance

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/awgmproto"
	"github.com/hoaxisr/awg-manager/internal/proxyrt"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/control"
)

// evictedProc — настоящий слушатель протокола: вытеснение делает сам сервер,
// когда подключается второй менеджер. Подделывать защёлку нечем — она
// приватна, и это правильно: снаружи её ставит только протокол.
type evictedProc struct{}

func (evictedProc) State() awgmproto.State           { return awgmproto.State{PID: os.Getpid()} }
func (evictedProc) AttachTun(string, *os.File) error { return awgmproto.ErrNotSupported }
func (evictedProc) DetachTun() error                 { return awgmproto.ErrNotSupported }

// RT10: защёлка вытеснения обязана сниматься на границе прогона.
//
// Снятие защёлки покрыто в control (TestLinkEvictedLatch зовёт ClearEvicted
// руками), а вот то, что её кто-то снимает в проде, — нет: удаление вызова из
// onState проходило зелёным. Цена — инстанс, мёртвый до перезапуска демона:
// после единственного вытеснения Link коротит любой State ошибкой ErrEvicted,
// то есть реконсиляция больше никогда не увидит процесс.
func TestOnState_ClearsEvictedLatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.sock")
	srv, err := awgmproto.Listen(awgmproto.ServerConfig{
		Path: path, Impl: "wt-client", Role: "client", Instance: "default",
		Handler: evictedProc{},
	})
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve()
	t.Cleanup(func() { _ = srv.Close() })

	link := control.NewLink(control.LinkOpts{
		Path: path, Impl: "wt-client", Role: "client", Instance: "default",
		Binary:     "/opt/bin/wt-client",
		Post:       func(proxyrt.EventKind) bool { return true },
		Log:        func(string) {},
		Alive:      func(int, string) bool { return true },
		RetryEvery: 10 * time.Millisecond, ConnectDeadline: 300 * time.Millisecond,
		CallTimeout: time.Second,
	})
	t.Cleanup(link.Close)
	if _, err := link.State(context.Background()); err != nil {
		t.Fatalf("связь не поднялась: %v", err)
	}

	// Второй менеджер вытесняет нас — тот самый сценарий, ради которого
	// защёлка существует.
	other, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	waitFor(t, link.Evicted)

	inst := New(Config{ID: "i1", Role: &recordRole{},
		Cfg:     func() any { return nil },
		Intent:  func() proxyrt.Intent { return proxyrt.IntentEnabled },
		Link:    link,
		States:  proxyrt.NewStateStore(nil, nil),
		Journal: &memJournal{},
	})
	ctx, cancel := contextWithCancel()
	defer cancel()
	inst.Start(ctx)
	inst.Post(proxyrt.EventBoot)
	defer inst.Stop()

	waitFor(t, func() bool { return !link.Evicted() })
}
