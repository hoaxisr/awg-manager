package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	ndmsquery "github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/opkgtun"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// NDMS отдаёт имена интерфейсов ТОЛЬКО в CamelCase и без поля kernel-имени —
// проверено на роутере 5.01.C.3.0-1: у OpkgTun10 есть лишь "id" и
// "interface-name", оба "OpkgTun10".
const ifaceListJSON = `{
  "OpkgTun10": {"id":"OpkgTun10","interface-name":"OpkgTun10","type":"OpkgTun","state":"up","link":"up","description":"vdsina30"},
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

func readHolders(t *testing.T, s opkgtun.Source) opkgtun.Taken {
	t.Helper()
	got, err := s.Read(context.Background())
	if err != nil {
		t.Fatalf("источник %s: %v", s.Name, err)
	}
	return got
}

// Записи NDMS — единственный источник, видящий номер, чьё устройство удалено:
// на стенде 5.01.C.3.0-1 запись переживает `ip link del` со state=error.
func TestNDMSHolders(t *testing.T) {
	a := newAdapterWith(t, ifaceListJSON, func() ([]int, error) { return nil, nil })

	got := readHolders(t, opkgtun.Source{Name: "ndms", Read: ndmsHolders(a)})
	if _, busy := got[10]; !busy {
		t.Fatalf("OpkgTun10 из NDMS должен считаться занятым, got %v", got)
	}
	// Имя NDMS у всех интерфейсов одной формы — опознать чужой можно только по
	// описанию, поэтому оно обязано попасть в имя держателя.
	name := got[10].String()
	if !strings.Contains(name, "OpkgTun10") || !strings.Contains(name, "vdsina30") {
		t.Errorf("держатель = %q, ждали имя интерфейса и его описание", name)
	}
	if len(got) != 1 {
		t.Errorf("посторонние интерфейсы не должны попадать в занятость, got %v", got)
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

// Занятость для ВЫДАЧИ номера объединяет все источники — в отличие от живой
// половины, которая отвечает на другой вопрос. Проверяется исходом выдачи:
// оба номера заняты, значит просителю достанется третий.
func TestOccupancyUnionsSysAndNDMS(t *testing.T) {
	a := newAdapterWith(t, ifaceListJSON, func() ([]int, error) { return []int{3}, nil })
	pool := opkgtun.NewPool(16,
		opkgtun.Source{Name: "записи NDMS", Read: ndmsHolders(a)},
		opkgtun.Source{Name: "живые интерфейсы", Read: liveHolders(a)},
	)

	// Выбираются ВСЕ свободные номера окна 2..16 разом: 3 держит устройство в
	// ядре, 10 — запись NDMS, значит свободных тринадцать, и ни один из двух
	// занятых в выдачу попасть не должен.
	reqs := make([]opkgtun.Request, 0, 13)
	for i := 0; i < 13; i++ {
		reqs = append(reqs, opkgtun.Want(opkgtun.ProxyHolder("wdtt-client:x"+strconv.Itoa(i), "", "")))
	}
	res, err := pool.Reserve(context.Background(), reqs...)
	if err != nil {
		t.Fatalf("выдача: %v", err)
	}
	defer res.Close()
	for _, busy := range []int{3, 10} {
		if slices.Contains(res.Numbers(), busy) {
			t.Errorf("номер %d отдан, хотя занят: занятость не объединяет /sys и NDMS", busy)
		}
	}
}

// Неполная занятость читается как «номер свободен» и ведёт к коллизии, поэтому
// сбой ЛЮБОГО поставщика обязан быть отказом, а не частичной картой. Имя
// поставщика в ошибке обязательно: «занятость не собрана» без него нечинимо.
func TestOccupancyFailsClosedAndNamesTheSource(t *testing.T) {
	ok := opkgtun.Source{Name: "живые", Read: func(context.Context) (opkgtun.Taken, error) {
		return opkgtun.Taken{3: opkgtun.LiveHolder(3)}, nil
	}}
	bad := opkgtun.Source{Name: "записи прокси", Read: func(context.Context) (opkgtun.Taken, error) {
		return nil, errors.New("хранилище недоступно")
	}}

	_, err := opkgtun.NewPool(16, ok, bad).Reserve(context.Background(),
		opkgtun.Want(opkgtun.TunnelHolder("", "новый")))
	if err == nil {
		t.Fatal("сбой поставщика обязан давать отказ, а не частичную карту")
	}
	if !strings.Contains(err.Error(), "записи прокси") {
		t.Errorf("ошибка = %q, ждали имя отказавшего поставщика", err)
	}
}

// Перечисление записей туннелей обязано быть СТРОГИМ. Прощающее чтение
// пропускает временно нечитаемый файл молча — номер такой записи выглядит
// свободным, и его выдают второй раз. Это единственное направление ошибки,
// приводящее к коллизии, поэтому здесь отказ, а не неполная карта.
func TestTunnelHoldersFailClosedOnUnreadableRecord(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("от root права файла не ограничивают")
	}
	e := newOccEnv(t)
	if err := e.awg.Create(&storage.AWGTunnel{ID: "awg10", Name: "Amsterdam"}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(e.dir, "tunnels", "awg10.json")
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })

	src := opkgtun.Source{Name: "записи туннелей", Read: tunnelHolders(e.awg)}
	got, err := src.Read(context.Background())
	if err == nil {
		t.Fatalf("нечитаемая запись отдана как «занятых нет»: %v", got)
	}
}
