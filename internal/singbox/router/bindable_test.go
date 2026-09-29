package router

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

type fakeBindable struct{ list []WANInterfaceInfo }

func (f fakeBindable) ListBindable(ctx context.Context) ([]WANInterfaceInfo, error) {
	return f.list, nil
}

func TestValidateBindInterfaceExists(t *testing.T) {
	s := &ServiceImpl{deps: Deps{BindableInterfaces: fakeBindable{list: []WANInterfaceInfo{
		{Name: "ipsec0", Label: "IPSec VPN", Up: true},
	}}}}

	if err := s.validateBindInterface(context.Background(), "ipsec0"); err != nil {
		t.Errorf("known interface rejected: %v", err)
	}
	if err := s.validateBindInterface(context.Background(), "nope0"); err == nil {
		t.Error("unknown interface should be rejected")
	}
}

func TestValidateBindInterface_NilLister(t *testing.T) {
	s := &ServiceImpl{deps: Deps{}}
	// With no lister wired, fall back to permissive (don't block creation).
	if err := s.validateBindInterface(context.Background(), "ipsec0"); err != nil {
		t.Errorf("nil lister should not block, got %v", err)
	}
}

func newSettingsBackedService(t *testing.T, list []WANInterfaceInfo) *ServiceImpl {
	t.Helper()
	st := storage.NewSettingsStore(t.TempDir())
	if _, err := st.Load(); err != nil {
		t.Fatal(err)
	}
	return &ServiceImpl{deps: Deps{Settings: st, BindableInterfaces: fakeBindable{list: list}}}
}

func TestValidateBindInterface_ForeignAbsentAccepted(t *testing.T) {
	s := newSettingsBackedService(t, nil)
	if err := s.deps.Settings.MarkForeignInterface("csqtt0"); err != nil {
		t.Fatal(err)
	}
	if err := s.validateBindInterface(context.Background(), "csqtt0"); err != nil {
		t.Fatalf("отмеченный отсутствующий отвергнут: %v", err)
	}
	if err := s.validateBindInterface(context.Background(), "zt9"); err == nil {
		t.Fatal("неотмеченный отсутствующий принят")
	}
}

// #961: однажды принятая привязка direct не перепроверяется на Update —
// интерфейс мог пропасть из списка (VPN отключён), а переименование от этого
// ломаться не должно. Сменённая привязка проверяется по списку роутера: ppp9
// в нём есть (и то, что его держит другой direct, не мешает), gone0 — нет,
// хотя его тоже держит другой direct.
var directBindLister = fakeBindable{list: []WANInterfaceInfo{{Name: "ppp9"}}}

func assertDirectBindUpdate(t *testing.T, update func(tag string, o Outbound) error) {
	t.Helper()
	if err := update("test", Outbound{Type: "direct", Tag: "test2", BindInterface: "nocli0"}); err != nil {
		t.Fatalf("переименование с прежней привязкой отвергнуто: %v", err)
	}
	if err := update("test2", Outbound{Type: "direct", Tag: "test2", BindInterface: "ppp9"}); err != nil {
		t.Fatalf("смена на пригодный интерфейс отвергнута: %v", err)
	}
	if err := update("test2", Outbound{Type: "direct", Tag: "test2", BindInterface: "gone0"}); err == nil {
		t.Fatal("смена на интерфейс вне списка роутера принята")
	}
}

func TestUpdateDirect_KeepsAcceptedBind(t *testing.T) {
	svc, dir := newOrchedTestService(t)
	svc.deps.BindableInterfaces = directBindLister
	cfg := `{"outbounds":[{"tag":"test","type":"direct","bind_interface":"nocli0"},{"tag":"other","type":"direct","bind_interface":"ppp9"},{"tag":"stale","type":"direct","bind_interface":"gone0"},{"tag":"direct","type":"direct"}],"route":{"final":"direct","rules":[]}}`
	if err := os.WriteFile(filepath.Join(dir, "20-router.json"), []byte(cfg), 0644); err != nil {
		t.Fatal(err)
	}
	assertDirectBindUpdate(t, func(tag string, o Outbound) error {
		return svc.UpdateCompositeOutbound(context.Background(), tag, o)
	})
}

func TestFakeIPUpdateDirect_KeepsAcceptedBind(t *testing.T) {
	svc, _ := newOrchedTestService(t)
	ctx := context.Background()
	if err := svc.deps.Settings.Update(func(cur *storage.Settings) error {
		cur.OpkgTun = &storage.OpkgTunState{Mode: storage.OpkgTunModeFakeIP, Provisioned: true, FakeIP: &storage.OpkgTunFakeIPData{Inet4Range: "198.18.0.0/15"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := svc.fakeipWithConfig(ctx, "outbounds", func(c *RouterConfig) error {
		c.Outbounds = append(c.Outbounds,
			Outbound{Tag: "test", Type: "direct", BindInterface: "nocli0"},
			Outbound{Tag: "other", Type: "direct", BindInterface: "ppp9"},
			Outbound{Tag: "stale", Type: "direct", BindInterface: "gone0"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	svc.deps.BindableInterfaces = directBindLister
	assertDirectBindUpdate(t, func(tag string, o Outbound) error {
		return svc.FakeIPUpdateCompositeOutbound(ctx, tag, o)
	})
}
