package api

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/events"
)

// Пульс — единственный способ узнать об обрыве, который клиент не закрыл
// штатно. Без него подписка живёт до TCP-keepalive, а ClientCount всё это
// время держит фоновые опросчики в полном темпе.
func TestEventsStream_SendsHeartbeat(t *testing.T) {
	prevBeat := sseHeartbeat
	sseHeartbeat = 40 * time.Millisecond
	t.Cleanup(func() { sseHeartbeat = prevBeat })

	bus := events.NewBus()
	srv := httptest.NewServer(http.HandlerFunc(NewEventsHandler(bus, "inst-1").Stream))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatalf("запрос: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("подключение: %v", err)
	}
	defer resp.Body.Close()

	rd := bufio.NewReader(resp.Body)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		line, err := rd.ReadString('\n')
		if err != nil {
			t.Fatalf("чтение потока: %v", err)
		}
		if strings.HasPrefix(line, ": ping") {
			return // пульс пришёл
		}
	}
	t.Fatal("пульс не пришёл — обрыв без FIN останется незамеченным")
}

// Закрытый клиент обязан освободить счётчик зрителей: по нему фоновые
// опросчики решают, нужна ли их работа.
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
