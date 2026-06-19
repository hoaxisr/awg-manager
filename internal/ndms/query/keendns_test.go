package query

import (
	"context"
	"net/http"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/ndms/transport"
)

// На современной прошивке /show/ndns отдаёт booked + domain раздельно;
// доменом для доступа служит их склейка booked.domain.
func TestKeenDNSFetch_BuildsFQDNFromBookedAndDomain(t *testing.T) {
	g := NewFakeGetter()
	g.SetRaw("/show/ndns", []byte(`{"name":"impod","booked":"impod","domain":"crazedns.ru","address":"91.144.142.72"}`))
	s := NewKeenDNSStore(g, NopLogger())

	info, err := s.Get(context.Background())
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if info == nil || info.Domain != "impod.crazedns.ru" {
		t.Fatalf("Domain=%v want impod.crazedns.ru", info)
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
