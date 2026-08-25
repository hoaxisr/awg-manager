package main

import (
	"context"
	"errors"
	"testing"

	ndmsquery "github.com/hoaxisr/awg-manager/internal/ndms/query"
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

// Половина занятости, приходящая из NDMS, обязана работать: это единственный
// источник, видящий интерфейс, который существует в NDMS без kernel-устройства.
func TestOpkgTunIndicesFromNDMS(t *testing.T) {
	a := newAdapterWith(t, ifaceListJSON, func() ([]int, error) { return nil, nil })

	got, err := a.LiveOpkgTunIndices(context.Background())
	if err != nil {
		t.Fatalf("LiveOpkgTunIndices: %v", err)
	}
	if !got[10] {
		t.Errorf("OpkgTun10 из NDMS должен считаться занятым, got %v", got)
	}
	if len(got) != 1 {
		t.Errorf("посторонние интерфейсы не должны попадать в занятость, got %v", got)
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

func TestOpkgTunIndicesUnionsSysAndNDMS(t *testing.T) {
	a := newAdapterWith(t, ifaceListJSON, func() ([]int, error) { return []int{3}, nil })

	got, err := a.LiveOpkgTunIndices(context.Background())
	if err != nil {
		t.Fatalf("LiveOpkgTunIndices: %v", err)
	}
	if !got[3] || !got[10] {
		t.Errorf("занятость должна объединять /sys и NDMS, got %v", got)
	}
}
