package router

import (
	"context"
	"testing"
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
