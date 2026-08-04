package router

import (
	"context"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
)

// Issue #488: в fakeip-режиме инспектор объяснял решения по конфигу, которого
// sing-box не грузит. После разделения генерации (5D0) картина другая:
// route-правила и наборы — общие для всех режимов, а режимным остаётся
// системный префикс правил и DNS-механизм fakeip. Инспектор обязан показывать
// merged-вид АКТИВНОГО режима: сначала режимный слот, потом общий — ровно в
// том порядке, в каком их сливает sing-box.

// seedInspectSlots раскладывает по слотам различимые конфиги: общий слот
// маршрутизирует shared.example → shared-out и держит DNS не-fakeip режимов
// (tproxy.example → dns-tproxy), режимный слот fakeip — свой DNS-механизм
// (fakeip.example → fakeip) и системное правило hijack-dns.
func seedInspectSlots(t *testing.T, svc *ServiceImpl) {
	t.Helper()
	// newFakeIPTestService enables only SlotFakeIP; enable SlotRouting too so
	// persistSlotDirect writes its file to the active path LoadEffective reads.
	if err := svc.deps.Orch.SetEnabled(orchestrator.SlotRouting, true); err != nil {
		t.Fatalf("enable SlotRouting: %v", err)
	}
	routerCfg := NewEmptyConfig()
	routerCfg.Route.Rules = []Rule{{Action: "route", DomainSuffix: []string{"shared.example"}, Outbound: "shared-out"}}
	routerCfg.DNS.Servers = []DNSServer{{Tag: "dns-tproxy", Type: "udp", Server: "1.1.1.1"}}
	routerCfg.DNS.Rules = []DNSRule{{Action: "route", DomainSuffix: []string{"tproxy.example"}, Server: "dns-tproxy"}}
	routerCfg.DNS.Final = "dns-tproxy"
	if err := svc.persistSlotDirect(orchestrator.SlotRouting, routerCfg, false); err != nil {
		t.Fatalf("persist router slot: %v", err)
	}

	fakeipCfg := NewEmptyConfig()
	fakeipCfg.Route.Final = ""
	fakeipCfg.Route.Rules = []Rule{{Action: "hijack-dns", Protocol: "dns"}}
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
	if err := svc.deps.Settings.Save(all); err != nil {
		t.Fatalf("settings save: %v", err)
	}
}

// Правила маршрутизации общие: один и тот же ответ в обоих режимах. Плюс
// системный префикс режимного слота обязан присутствовать в трассе — иначе
// инспектор снова объясняет решения по половине конфига.
func TestInspect_UsesSharedRulesInEveryMode(t *testing.T) {
	svc, _ := newFakeIPTestService(t) // RoutingMode: "fakeip-tun", both slots registered
	ctx := context.Background()
	seedInspectSlots(t, svc)

	res, err := svc.Inspect(ctx, InspectInput{Domain: "sub.shared.example"})
	if err != nil {
		t.Fatalf("Inspect (fakeip mode): %v", err)
	}
	if res.Destination != "shared-out" {
		t.Errorf("fakeip mode: Destination = %q, want shared-out (правила живут в общем слоте)", res.Destination)
	}
	// Системное правило режима идёт ПЕРВЫМ — как при слиянии config.d.
	if len(res.Matches) == 0 || res.Matches[0].Action != "hijack-dns" {
		t.Errorf("fakeip mode: разбор обязан начинаться системным правилом режима, получено %+v", res.Matches)
	}

	setRoutingMode(t, svc, "tproxy")
	res, err = svc.Inspect(ctx, InspectInput{Domain: "sub.shared.example"})
	if err != nil {
		t.Fatalf("Inspect (tproxy mode): %v", err)
	}
	if res.Destination != "shared-out" {
		t.Errorf("tproxy mode: Destination = %q, want shared-out", res.Destination)
	}
	// Режимный слот tproxy в этом харнессе пуст — системного префикса нет,
	// значит первым идёт пользовательское правило общего слота.
	if len(res.Matches) == 0 || res.Matches[0].Action == "hijack-dns" {
		t.Errorf("tproxy mode: чужой системный префикс попал в разбор: %+v", res.Matches)
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

	ch, err := svc.InspectStream(ctx, InspectInput{Domain: "sub.shared.example"})
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
	if result.Destination != "shared-out" {
		t.Errorf("stream fakeip mode: Destination = %q, want shared-out", result.Destination)
	}
}
