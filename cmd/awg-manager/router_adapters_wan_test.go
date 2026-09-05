package main

import (
	"context"
	"testing"

	ndmsquery "github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/singbox/router"
)

// Проекция ListWAN → router.WANInterfaceInfo: пять полей 1:1.
func TestRouterWANInterfaceAdapter_ListWAN_ProjectsAllFields(t *testing.T) {
	// БЕЗ "interface-name": кэш SystemName пуст, и kernelIfaceExists (os.Stat /sys/class/net —
	// хост; в пакете main TestMain-подмены нет) не вызывается; имя приходит POST-резолвером,
	// который фейк отдаёт через SetPostSystemName (getter.go:253-260).
	g := ndmsquery.NewFakeGetter()
	g.SetJSON("/show/interface/", `{"PPPoE0":{"id":"PPPoE0","type":"PPPoE","description":"Letai","state":"up","security-level":"public","priority":700,"summary":{"layer":{"ipv4":"running"}}}}`)
	g.SetPostSystemName("PPPoE0", `"ppp0"`)
	a := &routerWANInterfaceAdapter{store: ndmsquery.NewInterfaceStore(g, ndmsquery.NopLogger())}
	got, err := a.ListWAN(context.Background())
	if err != nil || len(got) != 1 {
		t.Fatalf("got %+v err %v", got, err)
	}
	want := router.WANInterfaceInfo{Name: "ppp0", ID: "PPPoE0", Label: "Letai", Up: true, Priority: 700}
	if got[0] != want {
		t.Fatalf("= %+v, want %+v", got[0], want)
	}
}
