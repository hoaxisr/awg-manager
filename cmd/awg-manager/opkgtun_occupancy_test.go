package main

import (
	"context"
	"errors"
	"testing"

	ndmsquery "github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// NDMS отдаёт имена интерфейсов ТОЛЬКО в CamelCase и без поля kernel-имени —
// проверено на роутере 5.01.C.3.0-1: у OpkgTun10 есть лишь "id" и
// "interface-name", оба "OpkgTun10".
const ifaceListJSON = `{
  "OpkgTun10": {"id":"OpkgTun10","interface-name":"OpkgTun10","type":"OpkgTun","state":"up","link":"up"},
  "GigabitEthernet0/0": {"id":"GigabitEthernet0/0","interface-name":"GigabitEthernet0/0","type":"Port","state":"up","link":"up"},
  "Wireguard0": {"id":"Wireguard0","interface-name":"Wireguard0","type":"Wireguard","state":"up","link":"up"}
}`

func newAdapterWith(t *testing.T, listJSON string, sys func() ([]int, error)) *routerOpkgTunIndexAdapter {
	t.Helper()
	fg := ndmsquery.NewFakeGetter()
	fg.SetJSON("/show/interface/", listJSON)
	return &routerOpkgTunIndexAdapter{
		store:   ndmsquery.NewInterfaceStore(fg, ndmsquery.NopLogger()),
		listSys: sys,
	}
}

// Пины NDMS — единственный источник, видящий номер, чьё устройство удалено:
// на стенде 5.01.C.3.0-1 запись переживает `ip link del` со state=error.
func TestNDMSOpkgTunPins(t *testing.T) {
	a := newAdapterWith(t, ifaceListJSON, func() ([]int, error) { return nil, nil })

	got, err := a.NDMSOpkgTunPins(context.Background())
	if err != nil {
		t.Fatalf("NDMSOpkgTunPins: %v", err)
	}
	if !got[10] {
		t.Errorf("OpkgTun10 из NDMS должен считаться занятым, got %v", got)
	}
	if len(got) != 1 {
		t.Errorf("посторонние интерфейсы не должны попадать в пины, got %v", got)
	}
}

// Живая половина отвечает на вопрос «что существует в ядре сейчас» — записи
// NDMS в неё попадать не должны, иначе охрана прочитает мёртвое устройство как
// живое и не станет пересоздавать туннель после краха.
func TestLiveIndicesAreKernelOnly(t *testing.T) {
	a := newAdapterWith(t, ifaceListJSON, func() ([]int, error) { return []int{3}, nil })

	got, err := a.LiveOpkgTunIndices(context.Background())
	if err != nil {
		t.Fatalf("LiveOpkgTunIndices: %v", err)
	}
	if got[10] {
		t.Errorf("запись NDMS без устройства не должна считаться живой, got %v", got)
	}
	if !got[3] || len(got) != 1 {
		t.Errorf("живой должна быть только kernel-половина, got %v", got)
	}
}

// Сбой чтения /sys — единственное направление, дающее недосчёт занятых номеров,
// то есть коллизию. Отказ обязателен: пустая карта читается как «всё свободно».
func TestOpkgTunIndicesFailsClosedOnSysError(t *testing.T) {
	a := newAdapterWith(t, ifaceListJSON, func() ([]int, error) {
		return nil, errors.New("read /sys/class/net: permission denied")
	})

	if _, err := a.LiveOpkgTunIndices(context.Background()); err == nil {
		t.Fatal("сбой /sys обязан давать ошибку, а не неполную занятость")
	}
}

// Занятость для ВЫДАЧИ номера объединяет оба источника — в отличие от живой
// половины, которая отвечает на другой вопрос.
func TestOccupancyUnionsSysAndNDMS(t *testing.T) {
	a := newAdapterWith(t, ifaceListJSON, func() ([]int, error) { return []int{3}, nil })

	got, err := storage.OpkgTunOccupancy(a, a.NDMSOpkgTunPins)(context.Background())
	if err != nil {
		t.Fatalf("OpkgTunOccupancy: %v", err)
	}
	if !got[3] || !got[10] {
		t.Errorf("занятость должна объединять /sys и NDMS, got %v", got)
	}
}
