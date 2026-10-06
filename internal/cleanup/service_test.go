package cleanup

import (
	"context"
	"testing"
	"time"

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

// exhaustingProbe — шаг уборки, съедающий общий ctx целиком.
type exhaustingProbe struct{}

func (exhaustingProbe) RemoveProbeHost(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}

// ctxSaver — что увидело финальное сохранение.
type ctxSaver struct {
	called bool
	err    error
}

func (s *ctxSaver) Save(ctx context.Context) error {
	s.called = true
	s.err = ctx.Err()
	return nil
}

// R1: шаги уборки съели общий ctx — финальное сохранение всё равно идёт со
// своим живым бюджетом: иначе Flush отказал бы до POST, и снятые записи
// вернулись бы сиротами после ребута роутера.
// Мутация: Save(ctx) с общим ctx → ctx.Err() != nil, красный.
func TestCleanupAll_FinalSaveOwnBudget(t *testing.T) {
	saver := &ctxSaver{}
	svc := New(nil, nil, nil, nil, nil, nil, nil, exhaustingProbe{}, saver)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	if err := svc.CleanupAll(ctx); err != nil {
		t.Fatalf("CleanupAll: %v", err)
	}
	if !saver.called || saver.err != nil {
		t.Fatalf("финальное сохранение: вызвано=%v ctx.Err=%v, ждали живой ctx", saver.called, saver.err)
	}
}
