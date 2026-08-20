package command

import (
	"context"
	"strings"
	"testing"
)

// Ответы сняты с живого роутера. NDMS отвечает HTTP 200 и прячет статус внутрь.
const (
	// Снятие кандидатуры дефолтного маршрута: роутер рапортует критическую
	// ошибку, а запись при этом реально снимает (стенд 91.144.142.72,
	// 2026-08-18 — запись после ответа исчезла из show/rc/ip/route).
	respDefaultRouteNetlink = `{"ip":{"route":{"status":[{"status":"error","code":"1",` +
		`"ident":"Io::Netlink","message":"file exists"}]}}}`
	// Снос dns-маршрута, чей интерфейс уже удалён.
	respDNSRouteNoSuchIface = `{"dns-proxy":{"route":[{"status":[{"status":"error","code":"4456948",` +
		`"ident":"Dns::Route::Manager","message":"no such interface: OpkgTun10."}]}]}}`
)

// Ложная ошибка снятия дефолтного маршрута не должна выглядеть провалом:
// иначе teardown policy-tun рапортует отказ там, где роутер всё сделал.
func TestRemoveDefaultRoute_ToleratesNetlinkLie(t *testing.T) {
	for _, tc := range []struct {
		name string
		call func(*RouteCommands) error
	}{
		{"v4", func(c *RouteCommands) error { return c.RemoveDefaultRoute(context.Background(), "OpkgTun0") }},
		{"v6", func(c *RouteCommands) error { return c.RemoveIPv6DefaultRoute(context.Background(), "OpkgTun0") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			poster := &fakePoster{}
			poster.SetResponse(respDefaultRouteNetlink)
			if err := tc.call(newRouteCommandsWith(poster)); err != nil {
				t.Errorf("ложная ошибка снятия считается провалом: %v", err)
			}
		})
	}
}

// Постановка дефолта такой поблажки не получает: там ошибка настоящая.
func TestSetDefaultRoute_SurfacesError(t *testing.T) {
	poster := &fakePoster{}
	poster.SetResponse(respDefaultRouteNetlink)
	if err := newRouteCommandsWith(poster).SetDefaultRoute(context.Background(), "OpkgTun0"); err == nil {
		t.Error("ошибка постановки дефолта проглочена")
	}
}

// Список на снос строится из живой выдачи роутера и может содержать маршрут с
// уже удалённым интерфейсом. Фатальная ошибка здесь останавливает весь синк
// DNS-маршрутов, и застрявшая запись не вычищается уже никогда.
func TestDeleteDNSRoutes_ToleratesVanishedInterface(t *testing.T) {
	poster := &fakePoster{}
	poster.SetResponse(respDNSRouteNoSuchIface)
	err := newDNSRouteCommandsWith(poster).DeleteRoutes(context.Background(),
		[]DNSRouteSpec{{Group: "g", Interface: "OpkgTun10"}})
	if err != nil {
		t.Errorf("снос маршрута с исчезнувшим интерфейсом считается провалом: %v", err)
	}
}

// Постановка dns-маршрута на несуществующий интерфейс — настоящая ошибка.
func TestUpsertDNSRoutes_SurfacesVanishedInterface(t *testing.T) {
	poster := &fakePoster{}
	poster.SetResponse(respDNSRouteNoSuchIface)
	err := newDNSRouteCommandsWith(poster).UpsertRoutes(context.Background(),
		[]DNSRouteSpec{{Group: "g", Interface: "OpkgTun10"}})
	if err == nil {
		t.Error("отказ постановки dns-маршрута проглочен")
	}
}

// NDMS применяет пакетный payload поэлементно: отвергнутый элемент не отменяет
// применённые. Кэши обязаны сброситься и сохранение — запроситься, иначе
// применённая половина живёт в running-config, но не доезжает до startup-config,
// а следующий diff считается по протухшему кэшу.
func TestPartialBatchFailure_StillInvalidatesAndSaves(t *testing.T) {
	poster := &fakePoster{}
	poster.SetResponse(respDNSRouteNoSuchIface)
	sc := newSaveFor(poster)
	q := testQueries()
	invalidated := false
	err := postMutationChecked(context.Background(), poster, sc,
		map[string]any{"x": 1}, "batch op",
		func() { invalidated = true },
		q.RunningConfig.InvalidateAll)
	if err == nil {
		t.Fatal("ошибка обязана всплыть")
	}
	if !invalidated {
		t.Error("кэши не сброшены после частичного применения")
	}
	if sc.Status().State != SaveStatePending {
		t.Errorf("сохранение не запрошено: state=%v", sc.Status().State)
	}
}

// Предикат «интерфейса нет» нужен вызывающим (восстановление NAT), чтобы
// отличать «нечего восстанавливать» от настоящего отказа.
func TestIsMissingInterfaceError(t *testing.T) {
	poster := &fakePoster{}
	poster.SetResponse(respNATNoIface)
	err := newNATCommandsWith(poster).SetSegmentNAT(context.Background(), "AwgmNoSeg")
	if err == nil {
		t.Fatal("ожидалась ошибка")
	}
	if !IsMissingInterfaceError(err) {
		t.Errorf("предикат не распознал «интерфейса нет»: %v", err)
	}
	other := &fakePoster{}
	other.SetResponse(`{"ip":{"nat":{"status":[{"status":"error","message":"busy"}]}}}`)
	if err := newNATCommandsWith(other).SetSegmentNAT(context.Background(), "Seg"); !strings.Contains(err.Error(), "busy") || IsMissingInterfaceError(err) {
		t.Errorf("предикат сработал на чужой ошибке: %v", err)
	}
}
