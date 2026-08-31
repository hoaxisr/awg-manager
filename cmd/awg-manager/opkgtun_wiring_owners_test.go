package main

import (
	"context"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// F16: номер, который держит запись прокси-инстанса без живого интерфейса и без
// записи NDMS, обязан считаться занятым И у выдающих номера туннелей, И у
// роутерных режимов — на mips/mipsel пулы всех троих пересекаются.

func noNDMSPins(context.Context) (map[int]bool, error) { return nil, nil }

func TestOpkgOccupancyAllOwners_SeesProxyRecordPin(t *testing.T) {
	e := newOccEnv(t)
	e.putRecord(t, rawClientRecord("de", "OpkgTun12", "opkgtun12"))

	occ := opkgOccupancyAllOwners(fakeLiveIfaces{live: map[int]bool{}}, noNDMSPins, e.awg, e.settings, e.store)
	got, err := occ(context.Background())
	if err != nil {
		t.Fatalf("occupancy: %v", err)
	}
	if !got[12] {
		t.Errorf("занятость = %v, want номер 12 занятым (пин записи прокси-инстанса)", got)
	}
}

func TestRouterForeignOpkgPins_SeesProxyRecordPin(t *testing.T) {
	e := newOccEnv(t)
	e.putRecord(t, rawClientRecord("de", "OpkgTun12", "opkgtun12"))
	if err := e.awg.Create(&storage.AWGTunnel{ID: "awg10", Name: "t10"}); err != nil {
		t.Fatalf("awg.Create: %v", err)
	}

	pins := routerForeignOpkgPins(e.awg, noNDMSPins, e.store)
	got, err := pins(context.Background())
	if err != nil {
		t.Fatalf("pins: %v", err)
	}
	if !got[12] {
		t.Errorf("пины = %v, want номер 12 занятым (запись прокси-инстанса)", got)
	}
	// Состав, а не один поставщик: запись туннеля обязана остаться видимой.
	if !got[10] {
		t.Errorf("пины = %v, want номер 10 занятым (запись туннеля)", got)
	}
}
