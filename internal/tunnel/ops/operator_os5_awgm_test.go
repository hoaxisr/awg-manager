package ops

import (
	"context"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
)

// Запись awgm<N> осталась от KeeneticOS 4.x: NewNames не даёт ей NDMS-имени.
// До гейта операторы уносили пустую строку прямо в RCI как идентификатор
// интерфейса. Здесь queries и commands — nil, поэтому любое обращение к NDMS
// упало бы паникой: тест краснеет и на снятом гейте, и на попытке обойти его
// сбоку.
func newOS5ForAWGM() *OperatorOS5Impl {
	return NewOperatorOS5(nil, nil, &MockWGClient{}, &MockBackend{}, &MockFirewall{})
}

func awgmConfig() tunnel.Config {
	return tunnel.Config{
		ID:      "awgm5",
		Name:    "legacy",
		Address: "10.8.0.2/24",
		MTU:     1420,
	}
}

func TestOS5ColdStartRejectsOS4Tunnel(t *testing.T) {
	o := newOS5ForAWGM()
	backend := o.backend.(*MockBackend)
	o.ipRun = mockIPRun

	err := o.ColdStart(context.Background(), awgmConfig())
	if err == nil {
		t.Fatal("ColdStart для awgm-записи обязан отказать, got nil")
	}
	if !strings.Contains(err.Error(), "4.x") {
		t.Errorf("ошибка должна называть причину, got: %v", err)
	}
	if len(backend.StartCalls) != 0 {
		t.Errorf("интерфейс не должен подниматься: %v", backend.StartCalls)
	}
}

func TestOS5ReconcileRejectsOS4Tunnel(t *testing.T) {
	o := newOS5ForAWGM()
	backend := o.backend.(*MockBackend)
	o.ipRun = mockIPRun

	err := o.Reconcile(context.Background(), awgmConfig())
	if err == nil {
		t.Fatal("Reconcile для awgm-записи обязан отказать, got nil")
	}
	if len(backend.StartCalls) != 0 {
		t.Errorf("интерфейс не должен подниматься: %v", backend.StartCalls)
	}
}

// Удаление обязано пройти: без него миграционное действие пользователя —
// «удалите и импортируйте конфиг заново» — невыполнимо.
func TestOS5DeleteSkipsNDMSForOS4Tunnel(t *testing.T) {
	o := newOS5ForAWGM()
	rec := &ipRunRecorder{}
	o.ipRun = rec.run

	stored := &storage.AWGTunnel{ID: "awgm5", Name: "legacy"}

	if err := o.Delete(context.Background(), stored); err != nil {
		t.Fatalf("Delete обязан пройти без NDMS, got: %v", err)
	}

	var deletedLink bool
	for _, call := range rec.Calls {
		if strings.Contains(call, "link del dev awgm5") {
			deletedLink = true
		}
	}
	if !deletedLink {
		t.Errorf("kernel-интерфейс должен быть снят, вызовы: %v", rec.Calls)
	}
}
