package singbox

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// safePublisher — fakePublisher из traffic_test.go не потокобезопасен, а здесь
// публикация идёт из цикла агрегатора.
type safePublisher struct {
	mu     sync.Mutex
	events []publishedEvent
}

func (p *safePublisher) Publish(event string, data any) {
	p.mu.Lock()
	p.events = append(p.events, publishedEvent{name: event, data: data})
	p.mu.Unlock()
}

func (p *safePublisher) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.events)
}

type safeFeeder struct {
	mu    sync.Mutex
	calls int
}

func (f *safeFeeder) Feed(string, int64, int64) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
}

func (f *safeFeeder) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

type trafficClients struct{ n atomic.Int64 }

func (c *trafficClients) ClientCount() int { return int(c.n.Load()) }

// clashStub изображает sing-box: шлёт полный снимок таблицы соединений часто,
// как настоящий Clash (раз в секунду в проде).
func clashStub(t *testing.T, every time.Duration) (addr string, sent *atomic.Int64) {
	t.Helper()
	sent = &atomic.Int64{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		for i := 0; ; i++ {
			msg := fmt.Sprintf(`{"memory":1,"downloadTotal":%d,"uploadTotal":%d,"connections":[{"chains":["A"],"upload":%d,"download":%d}]}`, i, i, i, i)
			if err := c.Write(r.Context(), websocket.MessageText, []byte(msg)); err != nil {
				return
			}
			sent.Add(1)
			select {
			case <-r.Context().Done():
				return
			case <-time.After(every):
			}
		}
	}))
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://"), sent
}

func newGateAggregator(t *testing.T, addr string, pub TrafficPublisher, feeder HistoryFeeder, clients ClientCounter) *TrafficAggregator {
	t.Helper()
	agg := NewTrafficAggregator(func() string { return addr }, pub, feeder)
	agg.interval = 20 * time.Millisecond // шаг публикации
	agg.SetClientCounter(clients)
	return agg
}

// Панель не открыта: SSE-публикаций быть не должно вовсе. А вот историю
// наполнять обязаны прежним темпом — источник немонотонный (суммы по тегам
// пересобираются только из открытых соединений), и растянутый шаг терял бы
// короткие соединения и целые окна на отрицательной дельте.
func TestTrafficAggregator_IdleSkipsPublishButFeedsHistory(t *testing.T) {
	addr, sent := clashStub(t, 10*time.Millisecond)
	pub := &safePublisher{}
	feeder := &safeFeeder{}
	agg := newGateAggregator(t, addr, pub, feeder, &trafficClients{}) // ноль зрителей

	ctx, cancel := context.WithCancel(context.Background())
	go agg.Run(ctx)
	time.Sleep(300 * time.Millisecond)
	cancel()

	if sent.Load() < 5 {
		t.Fatalf("заглушка отправила %d сообщений — тест ничего не проверяет", sent.Load())
	}
	if got := pub.count(); got != 0 {
		t.Errorf("SSE-публикаций %d, ожидалось 0 при закрытой панели", got)
	}
	if got := feeder.count(); got < 3 {
		t.Errorf("подач в историю %d, ожидалось ≥3: история наполняется и в простое", got)
	}
}

// Открытая панель — прежний темп: публикации идут.
func TestTrafficAggregator_WatchedPublishes(t *testing.T) {
	addr, _ := clashStub(t, 10*time.Millisecond)
	pub := &safePublisher{}
	clients := &trafficClients{}
	clients.n.Store(1)
	agg := newGateAggregator(t, addr, pub, &safeFeeder{}, clients)

	ctx, cancel := context.WithCancel(context.Background())
	go agg.Run(ctx)
	time.Sleep(300 * time.Millisecond)
	cancel()

	if got := pub.count(); got < 3 {
		t.Errorf("SSE-публикаций %d, ожидалось ≥3 при открытой панели", got)
	}
}
