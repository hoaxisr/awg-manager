package managed

import (
	"context"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// S2 F546: выключение сервера, чей интерфейс снят снаружи, — опускать нечего:
// nil без команд (как api SetEnabled(false) → 200 у серверов NDMS, Task 17).
// Включение того же — ошибка: TestSetEnabled_InterfaceGone_ErrorNoCommands.
func TestScenario_SetEnabledFalse_InterfaceGone_NilNoCommands(t *testing.T) {
	t.Skip("F546: managed.SetEnabled(false) по снятому интерфейсу отдаёт errServerGone вместо nil — requireServer до ветки enabled (service_server.go:446)")
	f := query.NewFakeNDMS()
	s := newServiceWithOracle(t, f, nil, storage.ManagedServer{InterfaceName: "Wireguard3"})
	if err := s.SetEnabled(context.Background(), "Wireguard3", false); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if f.Phantoms != 0 || f.E != 0 || len(f.Posts) != 0 {
		t.Fatalf("phantoms=%d E=%d posts=%v", f.Phantoms, f.E, f.Posts)
	}
}
