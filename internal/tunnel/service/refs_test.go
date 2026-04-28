// internal/tunnel/service/refs_test.go
package service

import (
	"context"
	"testing"
)

type fakeDeviceProxyRefs struct {
	references map[string]bool
}

func (f *fakeDeviceProxyRefs) HasSelectorReference(tag string) bool {
	return f.references[tag]
}

type fakeRouterRefs struct {
	rules map[string][]int
}

func (f *fakeRouterRefs) RulesReferencing(tag string) []int {
	return f.rules[tag]
}

func TestCheckTunnelReferences_NoRefs(t *testing.T) {
	dp := &fakeDeviceProxyRefs{}
	r := &fakeRouterRefs{}
	if err := checkTunnelReferences("tun-a", dp, r); err != nil {
		t.Errorf("expected nil, got %v", err)
	}
}

func TestCheckTunnelReferences_DeviceProxyRef(t *testing.T) {
	dp := &fakeDeviceProxyRefs{references: map[string]bool{"awg-tun-a": true}}
	r := &fakeRouterRefs{}
	err := checkTunnelReferences("tun-a", dp, r)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	refErr, ok := err.(ErrTunnelReferenced)
	if !ok {
		t.Fatalf("expected ErrTunnelReferenced, got %T", err)
	}
	if !refErr.DeviceProxy {
		t.Errorf("expected DeviceProxy=true, got %+v", refErr)
	}
	if len(refErr.RouterRules) != 0 {
		t.Errorf("expected no router rules, got %v", refErr.RouterRules)
	}
}

func TestCheckTunnelReferences_RouterRulesRef(t *testing.T) {
	dp := &fakeDeviceProxyRefs{}
	r := &fakeRouterRefs{rules: map[string][]int{"awg-tun-a": {3, 7}}}
	err := checkTunnelReferences("tun-a", dp, r)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	refErr := err.(ErrTunnelReferenced)
	if refErr.DeviceProxy {
		t.Errorf("expected DeviceProxy=false, got true")
	}
	if len(refErr.RouterRules) != 2 || refErr.RouterRules[0] != 3 || refErr.RouterRules[1] != 7 {
		t.Errorf("expected [3 7], got %v", refErr.RouterRules)
	}
}

func TestCheckTunnelReferences_BothRefs(t *testing.T) {
	dp := &fakeDeviceProxyRefs{references: map[string]bool{"awg-tun-a": true}}
	r := &fakeRouterRefs{rules: map[string][]int{"awg-tun-a": {1}}}
	err := checkTunnelReferences("tun-a", dp, r)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	refErr := err.(ErrTunnelReferenced)
	if !refErr.DeviceProxy || len(refErr.RouterRules) != 1 {
		t.Errorf("expected both flags set, got %+v", refErr)
	}
}

func TestCheckTunnelReferences_NilCheckers(t *testing.T) {
	if err := checkTunnelReferences("tun-a", nil, nil); err != nil {
		t.Errorf("nil checkers should yield no error, got %v", err)
	}
}

// Smoke-check that the Service.Delete path uses the helper.
func TestDelete_Refused_DeviceProxy(t *testing.T) {
	s := &ServiceImpl{
		deviceProxyRefs: &fakeDeviceProxyRefs{references: map[string]bool{"awg-tun-a": true}},
	}
	err := s.Delete(context.Background(), "tun-a")
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if _, ok := err.(ErrTunnelReferenced); !ok {
		t.Errorf("expected ErrTunnelReferenced, got %T (%v)", err, err)
	}
}
