package router

import (
	"context"
	"sync"
	"testing"
)

// Красный до фикса под -race: UpdateSettings пишет в живой объект кэша
// (settings.SingboxRouter = normalized) без лока, Snapshot маршалит кэш
// под RLock — лок только с одной стороны гонку не закрывает.
func TestUpdateSettings_NoSharedMutation(t *testing.T) {
	h := newTransitionHarness(t)
	ctx := context.Background()
	stop := make(chan struct{})
	var readers sync.WaitGroup
	// Несколько читателей: окно гонки — узкий зазор между Load и Save
	// внутри UpdateSettings, одиночный читатель попадает в него не каждый
	// прогон.
	for r := 0; r < 8; r++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				_, _ = h.store.Snapshot()
			}
		}()
	}
	sr, err := h.svc.GetSettings(ctx)
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	for i := 0; i < 100; i++ {
		// Меняем безобидное поле, чтобы каждый вызов реально сохранял.
		if i%2 == 0 {
			sr.BypassExtraPorts = "8080"
		} else {
			sr.BypassExtraPorts = ""
		}
		_ = h.svc.UpdateSettings(ctx, sr) // ошибки Reconcile не важны — ассерт по гонке
	}
	close(stop)
	readers.Wait()
}
