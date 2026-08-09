package accesspolicy

import (
	"context"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/ndms/query"
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

// Настоящий резолв обязан ходить за свежим хотспотом: устройство могло сменить
// аренду, а прежний адрес — достаться соседу, которому вытеснение снесло бы
// его собственные соединения.
func TestEvictDeviceFlowsRefreshesHotspotCache(t *testing.T) {
	const hotspotPath = "/show/ip/hotspot"
	fg := query.NewFakeGetter()
	fg.SetJSON(hotspotPath, `{"host":[{"ip":"192.168.1.10","mac":"aa:bb:cc:dd:ee:ff"}]}`)

	ev := &fakeEvictor{}
	svc := &ServiceImpl{
		queries: &query.Queries{Hotspot: query.NewHotspotStore(fg, query.NopLogger())},
		evictor: ev,
	}

	svc.evictDeviceFlows(context.Background(), "AA:BB:CC:DD:EE:FF")

	// Аренда сменилась: устройство переехало, прежний адрес занял сосед.
	fg.SetJSON(hotspotPath, `{"host":[{"ip":"192.168.1.77","mac":"aa:bb:cc:dd:ee:ff"}]}`)
	svc.evictDeviceFlows(context.Background(), "AA:BB:CC:DD:EE:FF")

	want := []string{"192.168.1.10", "192.168.1.77"}
	if len(ev.got) != len(want) || ev.got[0] != want[0] || ev.got[1] != want[1] {
		t.Fatalf("вытеснение ушло по кэшу вместо свежего хотспота: got %q, want %q", ev.got, want)
	}
}

func TestEvictDeviceFlowsSilentWithoutEvictor(t *testing.T) {
	svc := &ServiceImpl{}
	svc.hostIPByMAC = func(context.Context, string) string { return "192.168.1.55" }
	svc.evictDeviceFlows(context.Background(), "aa:bb:cc:dd:ee:ff") // не должно паниковать
}
