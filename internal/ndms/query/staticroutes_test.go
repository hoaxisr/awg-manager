package query

import (
	"context"
	"testing"
)

// Форма снята со стенда 5.02.A.11 (27.09.2026): массив объектов с comment;
// запись опущенного интерфейса тоже присутствует. Пустой список — `[]`.
func TestStaticRouteStore_ListParsesEntries(t *testing.T) {
	fg := NewFakeGetter()
	fg.SetJSON("/show/rc/ip/route", `[
		{"network":"192.168.77.0","mask":"255.255.255.0","interface":"Wireguard9","auto":true,"index":1,"comment":"awgm-peer:5+0I/P0V"},
		{"host":"10.0.0.5","interface":"PPPoE0","auto":true,"comment":"manual"}]`)
	s := NewStaticRouteStore(fg, NopLogger())
	got, err := s.List(context.Background())
	if err != nil || len(got) != 2 {
		t.Fatalf("got %v err %v", got, err)
	}
	if got[0].Network != "192.168.77.0" || got[0].Mask != "255.255.255.0" || got[0].Interface != "Wireguard9" || got[0].Comment != "awgm-peer:5+0I/P0V" {
		t.Fatalf("entry 0: %+v", got[0])
	}
	if got[1].Host != "10.0.0.5" || got[1].Network != "" {
		t.Fatalf("entry 1: %+v", got[1])
	}
}

func TestStaticRouteStore_EmptyAndSingleForms(t *testing.T) {
	fg := NewFakeGetter()
	fg.SetJSON("/show/rc/ip/route", `[]`)
	if got, err := NewStaticRouteStore(fg, NopLogger()).List(context.Background()); err != nil || len(got) != 0 {
		t.Fatalf("empty: %v %v", got, err)
	}
	fg.SetJSON("/show/rc/ip/route", `{"network":"192.168.77.0","mask":"255.255.255.0","interface":"Wireguard9"}`)
	if got, err := NewStaticRouteStore(fg, NopLogger()).List(context.Background()); err != nil || len(got) != 1 {
		t.Fatalf("single: %v %v", got, err)
	}
}

func TestNewQueries_WiresStaticRoutes(t *testing.T) {
	q := NewQueries(Deps{Getter: NewFakeGetter(), Logger: NopLogger()})
	if q.StaticRoutes == nil {
		t.Fatal("Queries.StaticRoutes не подключён")
	}
}
