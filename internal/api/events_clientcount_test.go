package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/events"
)

// На счётчике зрителей держатся все фоновые гейты: матрица, sysfs-поллер,
// поллер метрик и delay-проверка решают по нему, нужна ли их работа. Если
// закрытый клиент не освобождает счётчик, нагрузка не спадает вовсе.
func TestEventsStream_ClientCountReleasedOnDisconnect(t *testing.T) {
	bus := events.NewBus()
	srv := httptest.NewServer(http.HandlerFunc(NewEventsHandler(bus, "inst-1").Stream))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("подключение: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for bus.ClientCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if bus.ClientCount() != 1 {
		t.Fatalf("ClientCount=%d, ожидался 1 на открытом стриме", bus.ClientCount())
	}

	cancel()
	resp.Body.Close()

	deadline = time.Now().Add(3 * time.Second)
	for bus.ClientCount() != 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := bus.ClientCount(); got != 0 {
		t.Errorf("ClientCount=%d после отключения, ожидался 0", got)
	}
}
