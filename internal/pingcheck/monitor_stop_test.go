package pingcheck

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/httpprobe"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/sys/httpclient"
)

// parkedDoer держит HTTP-проверку, пока тест её не отпустит.
type parkedDoer struct{ entered, release chan struct{} }

func (d parkedDoer) Do(context.Context, httpclient.CallConfig) (*httpclient.Result, error) {
	d.entered <- struct{}{}
	<-d.release
	return nil, errors.New("no route")
}

// F628: StopMonitoring посреди тика. Цикл читал m.stopCh в select уже после
// того, как остановка обнулила поле, и ждал на nil-канале — wg.Wait
// останавливающего висел до отмены s.ctx (на проде — до выключения демона,
// под замком туннеля оркестратора).
func TestStopMonitoring_DuringTickDoesNotHang(t *testing.T) {
	orig := httpprobe.Client
	t.Cleanup(func() { httpprobe.Client = orig })
	d := parkedDoer{entered: make(chan struct{}), release: make(chan struct{})}
	httpprobe.Client = d
	stubRunCmd(t)
	stubIfaceUp(t, true)

	store := newTunnelStore(t)
	if err := store.Create(&storage.AWGTunnel{ID: "awg15", Name: "nl", Backend: "kernel",
		Peer:      storage.AWGPeer{PublicKey: "pk", Endpoint: "198.51.100.7:51820"},
		PingCheck: &storage.TunnelPingCheck{Enabled: true, Method: "http", Interval: 60, FailThreshold: 3},
	}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	lb := NewLogBuffer()
	t.Cleanup(lb.Stop)
	s := &Service{tunnels: store, logBuffer: lb, ctx: ctx, monitors: map[string]*tunnelMonitor{}}

	s.StartMonitoring("awg15", "nl")
	<-d.entered // первый тик внутри проверки

	stopped := make(chan struct{})
	go func() {
		s.StopMonitoring("awg15")
		close(stopped)
	}()
	// Остановка обнулила поле, пока тик ещё идёт.
	deadline := time.Now().Add(2 * time.Second)
	for {
		s.mu.RLock()
		_, alive := s.monitors["awg15"]
		s.mu.RUnlock()
		if !alive {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("StopMonitoring не снял монитор")
		}
		time.Sleep(time.Millisecond)
	}
	close(d.release)

	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("StopMonitoring висит: цикл ждёт на обнулённом stopCh")
	}
}
