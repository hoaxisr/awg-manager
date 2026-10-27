package router

import (
	"context"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
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

// checkWaitsForEdit: пока пользовательская правка стоит в мутаторе, op обязан
// ждать замка слота — иначе он пишет поверх неё или из-под неё (F741). op
// без замка доходит до конца раньше, чем правка отпущена.
func checkWaitsForEdit(t *testing.T, edit func(func(*RouterConfig) error) error, op func()) {
	t.Helper()
	opDone := make(chan struct{})
	if err := edit(func(c *RouterConfig) error {
		go func() { op(); close(opDone) }()
		select {
		case <-opDone:
			t.Error("system write finished while a user edit held the slot")
		case <-time.After(300 * time.Millisecond):
		}
		c.Outbounds = append(c.Outbounds, Outbound{Tag: "from-user", Type: "socks", Server: "192.0.2.3"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	<-opDone
}

func provisionFakeIP(t *testing.T, svc *ServiceImpl) {
	t.Helper()
	if err := svc.deps.Settings.Update(func(cur *storage.Settings) error {
		cur.OpkgTun = &storage.OpkgTunState{Mode: storage.OpkgTunModeFakeIP, Provisioned: true, Index: 0,
			FakeIP: &storage.OpkgTunFakeIPData{Inet4Range: "198.18.0.0/15"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestFakeIPSystemWrites_WaitForUserEdit(t *testing.T) {
	ctx := context.Background()
	for name, op := range map[string]func(*ServiceImpl){
		"healTunSettings": func(s *ServiceImpl) {
			s.healTunSettings(ctx, orchestrator.SlotFakeIP, storage.SingboxRouterSettings{})
		},
		"heal1140SlotMigration": func(s *ServiceImpl) { s.heal1140SlotMigration(ctx, orchestrator.SlotFakeIP) },
		"persistFakeIPConfigFresh": func(s *ServiceImpl) {
			_ = s.persistFakeIPConfigFresh(ctx, FakeIPTunSpec{Iface: "opkgtun0", TunAddr4: "172.18.0.1/30", Inet4Range: "198.18.0.0/15"})
		},
	} {
		t.Run(name, func(t *testing.T) {
			svc, _ := newOrchedTestService(t)
			provisionFakeIP(t, svc)
			checkWaitsForEdit(t, func(fn func(*RouterConfig) error) error {
				return svc.fakeipWithConfig(ctx, "outbounds", fn)
			}, func() { op(svc) })
		})
	}
}

// Запись при включении fakeip берёт свежий конфиг: правка, сделанная после
// начала включения, переживает её (F741).
func TestPersistFakeIPConfigFresh_KeepsUserEdit(t *testing.T) {
	svc, _ := newOrchedTestService(t)
	ctx := context.Background()
	provisionFakeIP(t, svc)
	if err := svc.fakeipWithConfig(ctx, "outbounds", func(c *RouterConfig) error {
		c.Outbounds = append(c.Outbounds, Outbound{Tag: "from-user", Type: "socks", Server: "192.0.2.3"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := svc.persistFakeIPConfigFresh(ctx, FakeIPTunSpec{Iface: "opkgtun0", TunAddr4: "172.18.0.1/30", Inet4Range: "198.18.0.0/15"}); err != nil {
		t.Fatal(err)
	}
	cfg, err := svc.loadFakeIPConfig()
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range cfg.Outbounds {
		if o.Tag == "from-user" {
			return
		}
	}
	t.Fatal("user edit lost by the enable write")
}

func TestStaging_WaitsForUserEdit(t *testing.T) {
	ctx := context.Background()
	for name, op := range map[string]func(*ServiceImpl){
		"ApplyStaging":   func(s *ServiceImpl) { _, _ = s.ApplyStaging(ctx) },
		"DiscardStaging": func(s *ServiceImpl) { _ = s.DiscardStaging(ctx) },
	} {
		t.Run(name, func(t *testing.T) {
			svc, _ := newOrchedTestService(t)
			checkWaitsForEdit(t, func(fn func(*RouterConfig) error) error {
				return svc.withConfig(ctx, "outbounds", fn)
			}, func() { op(svc) })
		})
	}
}
