package accesspolicy

import (
	"context"
	"fmt"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/ndms/query"
)

const hotspotPath = "/show/ip/hotspot"

type fakeEvictor struct {
	got []string
	// ctxErr — состояние контекста на момент вызова: вытеснение обязано
	// получать неотменяемый контекст.
	ctxErr error
}

func (f *fakeEvictor) EvictFlows(ctx context.Context, ips ...string) {
	f.ctxErr = ctx.Err()
	f.got = append(f.got, ips...)
}

// hotspotQueries собирает Queries с настоящим HotspotStore поверх фейкового
// геттера — свой шов резолва в проде ради этого не нужен.
func hotspotQueries(fg *query.FakeGetter) *query.Queries {
	return &query.Queries{Hotspot: query.NewHotspotStore(fg, query.NopLogger())}
}

// Регистр MAC приходит от NDMS и от вызывающего независимо, поэтому нормализуются
// обе стороны сравнения.
func TestEvictDeviceFlowsResolvesAddressByMAC(t *testing.T) {
	cases := []struct {
		name       string
		callerMAC  string
		hotspotMAC string
	}{
		{"вызывающий в верхнем регистре", "AA:BB:CC:DD:EE:FF", "aa:bb:cc:dd:ee:ff"},
		{"хотспот в верхнем регистре", "aa:bb:cc:dd:ee:ff", "AA:BB:CC:DD:EE:FF"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fg := query.NewFakeGetter()
			fg.SetJSON(hotspotPath, fmt.Sprintf(`{"host":[
				{"ip":"192.168.1.12","mac":"11:22:33:44:55:66"},
				{"ip":"192.168.1.55","mac":%q}
			]}`, tc.hotspotMAC))

			ev := &fakeEvictor{}
			svc := &ServiceImpl{queries: hotspotQueries(fg), evictor: ev}

			svc.evictDeviceFlows(context.Background(), tc.callerMAC)

			if len(ev.got) != 1 || ev.got[0] != "192.168.1.55" {
				t.Fatalf("вытеснение не вызвано для адреса устройства: %q", ev.got)
			}
		})
	}
}

func TestEvictDeviceFlowsSkipsUnknownAddress(t *testing.T) {
	fg := query.NewFakeGetter()
	fg.SetJSON(hotspotPath, `{"host":[{"ip":"192.168.1.12","mac":"11:22:33:44:55:66"}]}`)

	ev := &fakeEvictor{}
	svc := &ServiceImpl{queries: hotspotQueries(fg), evictor: ev}

	svc.evictDeviceFlows(context.Background(), "AA:BB:CC:DD:EE:FF")

	if len(ev.got) != 0 {
		t.Fatalf("вытеснение вызвано без известного адреса: %q", ev.got)
	}
}

// Настоящий резолв обязан ходить за свежим хотспотом: устройство могло сменить
// аренду, а прежний адрес — достаться соседу, которому вытеснение снесло бы
// его собственные соединения.
func TestEvictDeviceFlowsRefreshesHotspotCache(t *testing.T) {
	fg := query.NewFakeGetter()
	fg.SetJSON(hotspotPath, `{"host":[{"ip":"192.168.1.10","mac":"aa:bb:cc:dd:ee:ff"}]}`)

	ev := &fakeEvictor{}
	svc := &ServiceImpl{queries: hotspotQueries(fg), evictor: ev}

	svc.evictDeviceFlows(context.Background(), "AA:BB:CC:DD:EE:FF")

	// Аренда сменилась: устройство переехало, прежний адрес занял сосед.
	fg.SetJSON(hotspotPath, `{"host":[{"ip":"192.168.1.77","mac":"aa:bb:cc:dd:ee:ff"}]}`)
	svc.evictDeviceFlows(context.Background(), "AA:BB:CC:DD:EE:FF")

	want := []string{"192.168.1.10", "192.168.1.77"}
	if len(ev.got) != len(want) || ev.got[0] != want[0] || ev.got[1] != want[1] {
		t.Fatalf("вытеснение ушло по кэшу вместо свежего хотспота: got %q, want %q", ev.got, want)
	}
}

// Отмена HTTP-запроса не должна отменять вытеснение: политика в роутере уже
// сменена. Фейки контекст не читают, поэтому проверяем сам контракт — вниз
// уходит неотменённый контекст.
func TestEvictDeviceFlowsSurvivesRequestCancel(t *testing.T) {
	fg := query.NewFakeGetter()
	fg.SetJSON(hotspotPath, `{"host":[{"ip":"192.168.1.55","mac":"aa:bb:cc:dd:ee:ff"}]}`)

	ev := &fakeEvictor{}
	svc := &ServiceImpl{queries: hotspotQueries(fg), evictor: ev}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	svc.evictDeviceFlows(ctx, "AA:BB:CC:DD:EE:FF")

	if len(ev.got) != 1 || ev.got[0] != "192.168.1.55" {
		t.Fatalf("вытеснение не вызвано: %q", ev.got)
	}
	if ev.ctxErr != nil {
		t.Fatalf("вытеснение получило отменённый контекст: %v", ev.ctxErr)
	}
}

func TestEvictDeviceFlowsSilentWithoutEvictor(t *testing.T) {
	svc := &ServiceImpl{}
	svc.evictDeviceFlows(context.Background(), "aa:bb:cc:dd:ee:ff") // не должно паниковать
}
