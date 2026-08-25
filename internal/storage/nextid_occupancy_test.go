package storage

import (
	"context"
	"errors"
	"testing"
)

func occupancyOf(indices ...int) OpkgTunPins {
	m := make(map[int]bool, len(indices))
	for _, i := range indices {
		m[i] = true
	}
	return func(context.Context) (map[int]bool, error) { return m, nil }
}

// Номер kernel-туннеля — это и номер интерфейса OpkgTun, поэтому занятый чужой
// подсистемой индекс выдавать нельзя, даже если в нашем хранилище записи нет.
func TestNextAvailableIDKernelRespectsExternalOccupancy(t *testing.T) {
	got, err := nextAvailableID(nil, "kernel", true, occupancyOf(10, 11), context.Background())
	if err != nil {
		t.Fatalf("nextAvailableID: %v", err)
	}
	if got != "awg12" {
		t.Errorf("занятые снаружи 10 и 11 должны быть пропущены, got %q", got)
	}
}

// nativewg живёт как Wireguard<N> и номеров OpkgTun не занимает — он не обязан
// платить ни отказом, ни пропуском номера за чужую занятость.
func TestNextAvailableIDNativeWGIgnoresOccupancy(t *testing.T) {
	got, err := nextAvailableID(nil, "nativewg", true, occupancyOf(20, 21), context.Background())
	if err != nil {
		t.Fatalf("nextAvailableID: %v", err)
	}
	if got != "awg20" {
		t.Errorf("nativewg не смотрит на занятость индексов, got %q", got)
	}
}

// Тот же довод: на прошивке 4.x интерфейсы OpkgTun не существуют вовсе.
func TestNextAvailableIDOS4IgnoresOccupancy(t *testing.T) {
	got, err := nextAvailableID(nil, "kernel", false, nil, context.Background())
	if err != nil {
		t.Fatalf("nextAvailableID: %v", err)
	}
	if got != "awgm0" {
		t.Errorf("на 4.x занятость не спрашивается, got %q", got)
	}
}

func TestNextAvailableIDKernelFailsClosed(t *testing.T) {
	failing := OpkgTunPins(func(context.Context) (map[int]bool, error) {
		return nil, errors.New("NDMS недоступен")
	})

	if _, err := nextAvailableID(nil, "kernel", true, failing, context.Background()); err == nil {
		t.Fatal("сбой источника занятости обязан давать отказ, а не выдачу номера вслепую")
	}
}

// Отсутствие источника — не «занятых нет», а незаконченная проводка: молча
// вернуться к старому поведению значит выдать занятый номер.
func TestNextAvailableIDKernelRequiresOccupancy(t *testing.T) {
	if _, err := nextAvailableID(nil, "kernel", true, nil, context.Background()); err == nil {
		t.Fatal("kernel без источника занятости обязан отказать")
	}
}

func TestNextAvailableIDKernelSkipsOwnRecords(t *testing.T) {
	tunnels := []AWGTunnel{{ID: "awg10", Backend: "kernel"}}

	got, err := nextAvailableID(tunnels, "kernel", true, occupancyOf(11), context.Background())
	if err != nil {
		t.Fatalf("nextAvailableID: %v", err)
	}
	if got != "awg12" {
		t.Errorf("свои записи и внешняя занятость складываются, got %q", got)
	}
}
