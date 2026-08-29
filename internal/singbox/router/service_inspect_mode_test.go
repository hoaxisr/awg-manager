package router

import (
	"context"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// Issue #488: в fakeip-режиме инспектор маршрутов объяснял решения по
// правилам TPROXY-слота (20-router.json), а не по живому fakeip-слоту
// (21-fakeip.json). Тесты фиксируют выбор слота по persisted routingMode
// для всех трёх входов инспектора (Inspect / InspectDNS / InspectStream).

// seedInspectSlots writes DISTINCT configs into both slots so a wrong-slot
// read is unambiguous: the tproxy slot routes tproxy.example → tproxy-out,
// the fakeip slot routes fakeip.example → fakeip-out (each with its own DNS
// rule server tag as well).
func seedInspectSlots(t *testing.T, svc *ServiceImpl) {
	t.Helper()
	// newFakeIPTestService enables only SlotFakeIP; enable SlotRouter too so
	// persistSlotDirect writes its file to the active path LoadEffective reads.
	if err := svc.deps.Orch.SetEnabled(orchestrator.SlotRouter, true); err != nil {
		t.Fatalf("enable SlotRouter: %v", err)
	}
	routerCfg := NewEmptyConfig()
	routerCfg.Route.Rules = []Rule{{Action: "route", DomainSuffix: []string{"tproxy.example"}, Outbound: "tproxy-out"}}
	routerCfg.DNS.Servers = []DNSServer{{Tag: "dns-tproxy", Type: "udp", Server: "1.1.1.1"}}
	routerCfg.DNS.Rules = []DNSRule{{Action: "route", DomainSuffix: []string{"tproxy.example"}, Server: "dns-tproxy"}}
	routerCfg.DNS.Final = "dns-tproxy"
	if err := svc.persistSlotDirect(orchestrator.SlotRouter, routerCfg, false); err != nil {
		t.Fatalf("persist router slot: %v", err)
	}

	fakeipCfg := NewEmptyConfig()
	fakeipCfg.Route.Rules = []Rule{{Action: "route", DomainSuffix: []string{"fakeip.example"}, Outbound: "fakeip-out"}}
	fakeipCfg.DNS.Servers = []DNSServer{
		{Tag: "fakeip", Type: "fakeip", Inet4Range: "198.18.0.0/15"},
		{Tag: "real", Type: "udp", Server: "1.1.1.1"},
	}
	fakeipCfg.DNS.Rules = []DNSRule{{Action: "route", DomainSuffix: []string{"fakeip.example"}, Server: "fakeip"}}
	fakeipCfg.DNS.Final = "real"
	if err := svc.persistSlotDirect(orchestrator.SlotFakeIP, fakeipCfg, true); err != nil {
		t.Fatalf("persist fakeip slot: %v", err)
	}
}

// setRoutingMode flips the persisted routingMode without touching anything else.
func setRoutingMode(t *testing.T, svc *ServiceImpl, mode string) {
	t.Helper()
	all, err := svc.deps.Settings.Load()
	if err != nil {
		t.Fatalf("settings load: %v", err)
	}
	all.SingboxRouter.RoutingMode = mode
	if err := svc.deps.Settings.Update(func(cur *storage.Settings) error { *cur = *all; return nil }); err != nil {
		t.Fatalf("settings save: %v", err)
	}
}

func TestInspect_UsesSlotOfActiveRoutingMode(t *testing.T) {
	svc, _ := newFakeIPTestService(t) // RoutingMode: "fakeip-tun", both slots registered
	ctx := context.Background()
	seedInspectSlots(t, svc)

	// fakeip mode → fakeip slot rules.
	res, err := svc.Inspect(ctx, InspectInput{Domain: "sub.fakeip.example"})
	if err != nil {
		t.Fatalf("Inspect (fakeip mode): %v", err)
	}
	if res.Destination != "fakeip-out" {
		t.Errorf("fakeip mode: Destination = %q, want fakeip-out (walked wrong slot?)", res.Destination)
	}
	// The tproxy-only domain must NOT resolve to the tproxy outbound here.
	res, err = svc.Inspect(ctx, InspectInput{Domain: "sub.tproxy.example"})
	if err != nil {
		t.Fatalf("Inspect (fakeip mode, tproxy domain): %v", err)
	}
	if res.Destination == "tproxy-out" {
		t.Errorf("fakeip mode: tproxy-slot rule matched — inspector walked the tproxy slot")
	}

	// tproxy mode → router slot rules.
	setRoutingMode(t, svc, "tproxy")
	res, err = svc.Inspect(ctx, InspectInput{Domain: "sub.tproxy.example"})
	if err != nil {
		t.Fatalf("Inspect (tproxy mode): %v", err)
	}
	if res.Destination != "tproxy-out" {
		t.Errorf("tproxy mode: Destination = %q, want tproxy-out", res.Destination)
	}
}

func TestInspectDNS_UsesSlotOfActiveRoutingMode(t *testing.T) {
	svc, _ := newFakeIPTestService(t)
	ctx := context.Background()
	seedInspectSlots(t, svc)

	res, err := svc.InspectDNS(ctx, InspectDNSInput{Domain: "sub.fakeip.example"})
	if err != nil {
		t.Fatalf("InspectDNS (fakeip mode): %v", err)
	}
	if res.Server != "fakeip" {
		t.Errorf("fakeip mode: Server = %q, want fakeip (walked wrong slot?)", res.Server)
	}

	setRoutingMode(t, svc, "tproxy")
	res, err = svc.InspectDNS(ctx, InspectDNSInput{Domain: "sub.tproxy.example"})
	if err != nil {
		t.Fatalf("InspectDNS (tproxy mode): %v", err)
	}
	if res.Server != "dns-tproxy" {
		t.Errorf("tproxy mode: Server = %q, want dns-tproxy", res.Server)
	}
}

func TestInspectStream_UsesSlotOfActiveRoutingMode(t *testing.T) {
	svc, _ := newFakeIPTestService(t)
	ctx := context.Background()
	seedInspectSlots(t, svc)

	ch, err := svc.InspectStream(ctx, InspectInput{Domain: "sub.fakeip.example"})
	if err != nil {
		t.Fatalf("InspectStream: %v", err)
	}
	var result *InspectResult
	for ev := range ch {
		if ev.Type == "inspect-error" {
			t.Fatalf("inspect-error: %s", ev.Error)
		}
		if ev.Type == "result" {
			r := *ev.Result
			result = &r
		}
	}
	if result == nil {
		t.Fatal("stream ended without a result event")
	}
	if result.Destination != "fakeip-out" {
		t.Errorf("stream fakeip mode: Destination = %q, want fakeip-out", result.Destination)
	}
}
