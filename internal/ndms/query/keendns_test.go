package query

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms/transport"
)

// На современной прошивке /show/ndns отдаёт booked + domain раздельно;
// доменом для доступа служит их склейка booked.domain.
func TestKeenDNSFetch_BuildsFQDNFromBookedAndDomain(t *testing.T) {
	g := NewFakeGetter()
	g.SetRaw("/show/ndns", []byte(`{"name":"example","booked":"example","domain":"crazedns.ru","address":"203.0.113.72"}`))
	s := NewKeenDNSStore(g, NopLogger())

	info, err := s.Get(context.Background())
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if info == nil || info.Domain != "example.crazedns.ru" {
		t.Fatalf("Domain=%v want example.crazedns.ru", info)
	}
}

// Регрессия #376: дёргаем ТОЛЬКО /show/ndns, легаси-пути больше не зондируем
// (иначе они 404'ят и спамят журналы awgm и keenetic).
func TestKeenDNSFetch_OnlyQueriesShowNdns(t *testing.T) {
	g := NewFakeGetter()
	g.SetRaw("/show/ndns", []byte(`{"booked":"","domain":""}`)) // KeenDNS не настроен
	s := NewKeenDNSStore(g, NopLogger())

	info, err := s.Get(context.Background())
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if info != nil {
		t.Fatalf("info=%v want nil (не настроено)", info)
	}
	if n := g.Calls("/show/sc/ndns"); n != 0 {
		t.Errorf("/show/sc/ndns вызван %d раз, ожидалось 0", n)
	}
	if n := g.Calls("/show/ip/dns/domain"); n != 0 {
		t.Errorf("/show/ip/dns/domain вызван %d раз, ожидалось 0", n)
	}
}

// 404 на /show/ndns = подсистема KeenDNS отсутствует на этой OS → «не
// настроено», а не ошибка (чтобы поллер не сыпал ошибками каждый тик).
func TestKeenDNSFetch_NotFoundIsNotError(t *testing.T) {
	g := NewFakeGetter()
	g.SetError("/show/ndns", &transport.HTTPError{Method: "GET", Path: "/show/ndns", Status: http.StatusNotFound})
	s := NewKeenDNSStore(g, NopLogger())

	info, err := s.Get(context.Background())
	if err != nil {
		t.Fatalf("404 должен трактоваться как nil,nil, получили err: %v", err)
	}
	if info != nil {
		t.Fatalf("info=%v want nil", info)
	}
}

// После 404 подсистемы на этой прошивке нет, и спрашивать её повторно незачем:
// истёкший TTL кэша не должен возвращать нас в RCI. Без защёлки заведомо
// провальный GET уходил раз в минуту и каждый писал ERROR в журнал.
func TestKeenDNSFetch_NotFoundBacksOffAndHeals(t *testing.T) {
	g := NewFakeGetter()
	g.SetError("/show/ndns", &transport.HTTPError{Method: "GET", Path: "/show/ndns", Status: http.StatusNotFound})
	s := NewKeenDNSStore(g, NopLogger())

	if _, err := s.Get(context.Background()); err != nil {
		t.Fatalf("первый Get: %v", err)
	}
	s.InvalidateAll() // эквивалент истёкшего TTL

	info, err := s.Get(context.Background())
	if err != nil {
		t.Fatalf("второй Get: %v", err)
	}
	if info != nil {
		t.Fatalf("info=%v want nil", info)
	}
	if n := g.Calls("/show/ndns"); n != 1 {
		t.Errorf("/show/ndns запрошен %d раз, ожидался 1 (бэкофф)", n)
	}

	// Бэкофф не вечен: прошивка с живой подсистемой обязана вылечиться сама.
	// Вечная защёлка на ложном 404 стартового окна заперла бы KeenDNS до
	// перезапуска демона, и выданные клиентам .conf ушли бы с WAN-адресом.
	s.absentMu.Lock()
	s.absentUntil = time.Now().Add(-time.Second)
	s.absentMu.Unlock()
	g.SetError("/show/ndns", nil) // подсистема отвечает
	g.SetRaw("/show/ndns", []byte(`{"booked":"example","domain":"crazedns.ru"}`))
	s.InvalidateAll()

	info, err = s.Get(context.Background())
	if err != nil {
		t.Fatalf("после истечения бэкоффа: %v", err)
	}
	if info == nil || info.Domain != "example.crazedns.ru" {
		t.Errorf("info=%v — бэкофф не истёк, подсистема заперта навсегда", info)
	}
}
