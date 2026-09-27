package router

import (
	"context"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

type fakeBindable struct{ list []WANInterfaceInfo }

func (f fakeBindable) ListBindable(ctx context.Context) ([]WANInterfaceInfo, error) {
	return f.list, nil
}

func (f fakeBindable) ListAllBindable(ctx context.Context) ([]WANInterfaceInfo, error) {
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

func TestListIngressEligible_ExcludesForeign(t *testing.T) {
	s := newSettingsBackedService(t, []WANInterfaceInfo{
		{Name: "ipsec0", Type: "IPSec"},
		{Name: "csqtt0", Foreign: true},
	})
	got, err := s.ListIngressEligibleInterfaces(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "ipsec0" {
		t.Fatalf("ingress = %+v", got)
	}
}
