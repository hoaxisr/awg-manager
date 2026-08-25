package storage

import (
	"context"
	"errors"
	"testing"
)

type fakeLister struct {
	indices map[int]bool
	err     error
}

func (f fakeLister) LiveOpkgTunIndices(context.Context) (map[int]bool, error) {
	return f.indices, f.err
}

func pins(m map[int]bool, err error) OpkgTunPins {
	return func(context.Context) (map[int]bool, error) { return m, err }
}

func TestOpkgTunOccupancyUnionsLiveAndPins(t *testing.T) {
	occ := OpkgTunOccupancy(
		fakeLister{indices: map[int]bool{3: true}},
		pins(map[int]bool{10: true}, nil),
		pins(map[int]bool{17: true}, nil),
	)

	got, err := occ(context.Background())
	if err != nil {
		t.Fatalf("OpkgTunOccupancy: %v", err)
	}
	for _, idx := range []int{3, 10, 17} {
		if !got[idx] {
			t.Errorf("номер %d должен быть занят, got %v", idx, got)
		}
	}
}

// Неполная занятость читается как «номер свободен» и ведёт к коллизии, поэтому
// сбой ЛЮБОГО поставщика обязан быть отказом, а не частичной картой.
func TestOpkgTunOccupancyFailsClosedOnPinsError(t *testing.T) {
	occ := OpkgTunOccupancy(
		fakeLister{indices: map[int]bool{3: true}},
		pins(nil, errors.New("хранилище недоступно")),
	)

	if _, err := occ(context.Background()); err == nil {
		t.Fatal("сбой поставщика пинов обязан давать отказ")
	}
}

func TestOpkgTunOccupancyFailsClosedOnLiveError(t *testing.T) {
	occ := OpkgTunOccupancy(fakeLister{err: errors.New("/sys недоступен")})

	if _, err := occ(context.Background()); err == nil {
		t.Fatal("сбой живого источника обязан давать отказ")
	}
}

func TestOpkgTunOccupancyWithoutPins(t *testing.T) {
	occ := OpkgTunOccupancy(fakeLister{indices: map[int]bool{5: true}})

	got, err := occ(context.Background())
	if err != nil {
		t.Fatalf("OpkgTunOccupancy: %v", err)
	}
	if len(got) != 1 || !got[5] {
		t.Errorf("без поставщиков занятость равна живой половине, got %v", got)
	}
}

// Удерживающая запись — Provisioned=false при непустой записи: номер держится
// ради permit'а пользователя, интерфейса при этом нет. Нулевой номер валиден.
func TestSettingsOpkgTunPins(t *testing.T) {
	tests := []struct {
		name   string
		fakeip *FakeIPState
		policy *PolicyTunState
		want   map[int]bool
	}{
		{"записей нет", nil, nil, map[int]bool{}},
		{"hold с нулевым номером", nil, &PolicyTunState{Index: 0}, map[int]bool{0: true}},
		{"provisioned", &FakeIPState{Provisioned: true, Index: 7}, nil, map[int]bool{7: true}},
		{"обе записи", &FakeIPState{Index: 2}, &PolicyTunState{Index: 5}, map[int]bool{2: true, 5: true}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := opkgTunPinsOf(tt.fakeip, tt.policy)
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for idx := range tt.want {
				if !got[idx] {
					t.Errorf("номер %d должен быть занят, got %v", idx, got)
				}
			}
		})
	}
}
