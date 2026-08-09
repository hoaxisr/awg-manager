package accesspolicy

import (
	"context"
	"testing"
)

type fakeEvictor struct{ got []string }

func (f *fakeEvictor) EvictFlows(_ context.Context, ips ...string) {
	f.got = append(f.got, ips...)
}

func TestEvictDeviceFlowsResolvesAddressByMAC(t *testing.T) {
	ev := &fakeEvictor{}
	svc := &ServiceImpl{evictor: ev}
	svc.hostIPByMAC = func(context.Context, string) string { return "192.168.1.55" }

	svc.evictDeviceFlows(context.Background(), "AA:BB:CC:DD:EE:FF")

	if len(ev.got) != 1 || ev.got[0] != "192.168.1.55" {
		t.Fatalf("вытеснение не вызвано для адреса устройства: %v", ev.got)
	}
}

func TestEvictDeviceFlowsSkipsUnknownAddress(t *testing.T) {
	ev := &fakeEvictor{}
	svc := &ServiceImpl{evictor: ev}
	svc.hostIPByMAC = func(context.Context, string) string { return "" }

	svc.evictDeviceFlows(context.Background(), "AA:BB:CC:DD:EE:FF")

	if len(ev.got) != 0 {
		t.Fatalf("вытеснение вызвано без известного адреса: %q", ev.got)
	}
}

func TestEvictDeviceFlowsSilentWithoutEvictor(t *testing.T) {
	svc := &ServiceImpl{}
	svc.hostIPByMAC = func(context.Context, string) string { return "192.168.1.55" }
	svc.evictDeviceFlows(context.Background(), "aa:bb:cc:dd:ee:ff") // не должно паниковать
}
