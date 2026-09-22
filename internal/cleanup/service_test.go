package cleanup

import (
	"context"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/testutil"
)

func TestMain(m *testing.M) { testutil.Main(m) }

// recorder фиксирует порядок шагов снос/сохранение.
type recorder struct{ steps []string }

func (r *recorder) RemoveProbeHost(context.Context) error {
	r.steps = append(r.steps, "remove-probe-host")
	return nil
}

func (r *recorder) Save(context.Context) error {
	r.steps = append(r.steps, "save")
	return nil
}

// Запись пробы обязана сниматься ДО сохранения конфигурации: сохранение —
// единственное, что переживает удаление пакета, иначе `ip host
// awgm-dnscheck.test` остаётся в startup-config роутера навсегда (#942).
func TestCleanupAll_RemovesProbeHostBeforeSave(t *testing.T) {
	rec := &recorder{}
	svc := New(nil, nil, nil, nil, nil, nil, nil, rec, rec)

	if err := svc.CleanupAll(context.Background()); err != nil {
		t.Fatalf("CleanupAll: %v", err)
	}

	want := []string{"remove-probe-host", "save"}
	if len(rec.steps) != len(want) {
		t.Fatalf("шаги: got %v, want %v", rec.steps, want)
	}
	for i := range want {
		if rec.steps[i] != want[i] {
			t.Fatalf("шаги: got %v, want %v", rec.steps, want)
		}
	}
}
