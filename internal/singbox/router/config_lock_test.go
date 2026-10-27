package router

import (
	"context"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// checkNoLostUpdate: правка A прочитала конфиг и стоит в мутаторе, пока
// правка B идёт целиком. Без замка B читает тот же снимок, A затирает её
// запись при сохранении (F727). С замком B ждёт A — в конфиге оба выхода.
// A ждёт B не дольше 300 мс: с замком B закончить раньше A не может.
func checkNoLostUpdate(t *testing.T, edit func(func(*RouterConfig) error) error, load func() (*RouterConfig, error)) {
	t.Helper()
	inside := make(chan struct{})
	bDone := make(chan struct{})
	aErr := make(chan error, 1)
	go func() {
		aErr <- edit(func(c *RouterConfig) error {
			close(inside)
			select {
			case <-bDone:
			case <-time.After(300 * time.Millisecond):
			}
			c.Outbounds = append(c.Outbounds, Outbound{Tag: "from-a", Type: "socks", Server: "192.0.2.1"})
			return nil
		})
	}()
	<-inside
	if err := edit(func(c *RouterConfig) error {
		c.Outbounds = append(c.Outbounds, Outbound{Tag: "from-b", Type: "socks", Server: "192.0.2.2"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	close(bDone)
	if err := <-aErr; err != nil {
		t.Fatal(err)
	}
	cfg, err := load()
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, o := range cfg.Outbounds {
		seen[o.Tag] = true
	}
	if !seen["from-a"] || !seen["from-b"] {
		t.Fatalf("parallel edit lost: from-a=%v from-b=%v", seen["from-a"], seen["from-b"])
	}
}

func TestWithConfig_ParallelEditsKeepBoth(t *testing.T) {
	svc, _ := newOrchedTestService(t)
	ctx := context.Background()
	checkNoLostUpdate(t, func(fn func(*RouterConfig) error) error {
		return svc.withConfig(ctx, "outbounds", fn)
	}, svc.loadRouterConfig)
}

func TestFakeIPWithConfig_ParallelEditsKeepBoth(t *testing.T) {
	svc, _ := newOrchedTestService(t)
	ctx := context.Background()
	// fakeipWithConfig требует provisioned-состояния.
	if err := svc.deps.Settings.Update(func(cur *storage.Settings) error {
		cur.OpkgTun = &storage.OpkgTunState{Mode: storage.OpkgTunModeFakeIP, Provisioned: true, Index: 0,
			FakeIP: &storage.OpkgTunFakeIPData{Inet4Range: "198.18.0.0/15"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	checkNoLostUpdate(t, func(fn func(*RouterConfig) error) error {
		return svc.fakeipWithConfig(ctx, "outbounds", fn)
	}, svc.loadFakeIPConfig)
}
