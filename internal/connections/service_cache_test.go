package connections

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/routing"
)

// connLine собирает строку conntrack с заданным источником и объёмом.
func connLine(src string, bytes int) string {
	return fmt.Sprintf(`ipv4     2 tcp      6 1187 ESTABLISHED src=%s dst=185.199.110.133 sport=49158 dport=443 packets=14 bytes=%d src=185.199.110.133 dst=172.16.0.2 sport=443 dport=49158 packets=12 bytes=6182 [ASSURED] mark=0 nmark=0 sc=0 ifw=59 ifl=35 mac=b0:4a:b4:74:80:f8 slan attrs= use=2`, src, bytes)
}

// ctxNDMS отвечает данными, пока контекст жив, и отказом на отменённом —
// как настоящий RCI-клиент. Нужен, чтобы отмена была НАБЛЮДАЕМА: без него
// обогащение падает всегда и тест не отличает отвязанный контекст от запросного.
type ctxNDMS struct{ calls atomic.Int64 }

func (c *ctxNDMS) GetStream(ctx context.Context, path string, fn func(io.Reader) error) error {
	c.calls.Add(1)
	if err := ctx.Err(); err != nil {
		return err
	}
	if path != "/show/ip/hotspot" {
		return errors.New("этот путь тесту не нужен")
	}
	const body = `{"host":[{"mac":"b0:4a:b4:74:80:f8","name":"ноутбук","hostname":"laptop"}]}`
	return fn(strings.NewReader(body))
}

// withConntrack подменяет путь к conntrack на временный файл и возвращает
// функцию перезаписи его содержимого.
func withConntrack(t *testing.T, lines ...string) func(...string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "nf_conntrack")
	write := func(ls ...string) {
		body := ""
		for _, l := range ls {
			body += l + "\n"
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatalf("запись conntrack: %v", err)
		}
	}
	write(lines...)

	prev := conntrackPath
	conntrackPath = path
	t.Cleanup(func() { conntrackPath = prev })
	return write
}

// emptyCatalog — каталог без туннелей: обогащение работает, соединения
// классифицируются как прямые. Больше для этих тестов и не нужно.
type emptyCatalog struct{}

func (emptyCatalog) ListAll(context.Context) []routing.TunnelEntry { return nil }
func (emptyCatalog) ResolveInterface(context.Context, string) (string, error) {
	return "", nil
}
func (emptyCatalog) Exists(context.Context, string) bool { return false }
func (emptyCatalog) GetKernelIface(context.Context, string) (string, bool) {
	return "", false
}
func (emptyCatalog) SnapshotAll(context.Context) *routing.RoutingSnapshot { return nil }
func (emptyCatalog) GetKernelIfaceName(context.Context, string) (string, error) {
	return "", nil
}

// noNDMS — RCI недоступен: сервис обязан пережить это и отдать соединения
// без имён клиентов (в проде ровно этот путь и берётся, когда ndm молчит).
type noNDMS struct{}

func (noNDMS) GetStream(context.Context, string, func(io.Reader) error) error {
	return errors.New("ndms недоступен")
}

func newCacheTestService(t *testing.T) *Service {
	t.Helper()
	svc := NewService(emptyCatalog{}, noNDMS{}, nil, nil)
	// Локальных адресов интерфейсов в тесте нет — иначе полезли бы в ядро.
	svc.ifaceAddrs = func(string) ([]net.Addr, error) { return nil, nil }
	return svc
}

// Повторный запрос не перечитывает conntrack: разбор ВСЕЙ таблицы и обогащение
// каждой записи — самая дорогая часть List, а вкладка гоняет её на каждую смену
// фильтра, сортировки и страницы.
func TestList_SecondCallServedFromCache(t *testing.T) {
	rewrite := withConntrack(t, connLine("192.168.1.15", 3389))
	svc := newCacheTestService(t)
	ctx := context.Background()

	first, err := svc.List(ctx, ListParams{})
	if err != nil {
		t.Fatalf("первый List: %v", err)
	}
	if first.Pagination.Total != 1 {
		t.Fatalf("записей %d, ожидалась 1", first.Pagination.Total)
	}

	// Таблица изменилась, но окно кэша ещё не истекло.
	rewrite(connLine("192.168.1.15", 3389), connLine("192.168.1.16", 100))

	second, err := svc.List(ctx, ListParams{})
	if err != nil {
		t.Fatalf("второй List: %v", err)
	}
	if second.Pagination.Total != 1 {
		t.Errorf("записей %d, ожидалась 1: второй запрос обязан взять кэш", second.Pagination.Total)
	}

	// Сброс кэша — и та же подмена наконец видна. Без этой проверки тест выше
	// был бы зелёным и на сломанном разборе второй строки.
	svc.InvalidateSnapshot()
	third, err := svc.List(ctx, ListParams{})
	if err != nil {
		t.Fatalf("третий List: %v", err)
	}
	if third.Pagination.Total != 2 {
		t.Errorf("после сброса кэша записей %d, ожидалось 2", third.Pagination.Total)
	}
}

// Сортировка идёт по отфильтрованной копии и НЕ должна переупорядочивать
// кэшированный набор: иначе порядок одного запроса протёк бы в следующий.
// applyFilters сегодня всегда строит новый срез — тест держит это свойство.
func TestList_CacheNotReordered(t *testing.T) {
	withConntrack(t,
		connLine("192.168.1.10", 100),
		connLine("192.168.1.20", 900),
		connLine("192.168.1.30", 500),
	)
	svc := newCacheTestService(t)
	ctx := context.Background()

	if _, err := svc.List(ctx, ListParams{SortBy: "bytes", SortDir: "desc"}); err != nil {
		t.Fatalf("сортированный List: %v", err)
	}

	plain, err := svc.List(ctx, ListParams{})
	if err != nil {
		t.Fatalf("несортированный List: %v", err)
	}
	want := []string{"192.168.1.10", "192.168.1.20", "192.168.1.30"}
	if len(plain.Connections) != len(want) {
		t.Fatalf("записей %d, ожидалось %d", len(plain.Connections), len(want))
	}
	for i, w := range want {
		if got := plain.Connections[i].Src; got != w {
			t.Errorf("позиция %d: src=%s, ожидался %s — кэш переупорядочен сортировкой", i, got, w)
		}
	}
}

// Отказ чтения conntrack обязан дойти до пользователя как отказ. Прежний общий
// cache.ListStore отдавал бы последний снимок БЕЗ срока годности, и вкладка
// бессрочно показывала бы старьё как актуальное — молчаливый отказ вместо 503.
func TestList_ReadFailureIsNotMaskedByStaleCache(t *testing.T) {
	rewrite := withConntrack(t, connLine("192.168.1.15", 3389))
	svc := newCacheTestService(t)
	ctx := context.Background()

	if _, err := svc.List(ctx, ListParams{}); err != nil {
		t.Fatalf("первый List: %v", err)
	}

	// Файл исчез, кэш сброшен — отдать обязаны ошибку, а не прежний снимок.
	svc.InvalidateSnapshot()
	_ = rewrite
	if err := os.Remove(conntrackPath); err != nil {
		t.Fatalf("удаление conntrack: %v", err)
	}

	if _, err := svc.List(ctx, ListParams{}); err == nil {
		t.Error("List вернул успех при нечитаемом conntrack — отказ замаскирован кэшем")
	}
}

// Отменённый контекст запроса не должен обрывать сборку: снимок ложится в кэш и
// достаётся соседям, а браузер рвёт предыдущий запрос на каждом клике по фильтру.
// Проверяем по НАБЛЮДАЕМОМУ следствию — обогащение именем клиента должно
// отработать, иначе в кэш лёг бы обеднённый снимок и его получили бы все.
func TestList_CancelledRequestStillEnriches(t *testing.T) {
	withConntrack(t, connLine("192.168.1.15", 3389))
	ndms := &ctxNDMS{}
	svc := NewService(emptyCatalog{}, ndms, nil, nil)
	svc.ifaceAddrs = func(string) ([]net.Addr, error) { return nil, nil }

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // отменён ДО вызова

	res, err := svc.List(ctx, ListParams{})
	if err != nil {
		t.Fatalf("List на отменённом контексте: %v", err)
	}
	if res.Pagination.Total != 1 {
		t.Fatalf("записей %d, ожидалась 1", res.Pagination.Total)
	}
	if ndms.calls.Load() == 0 {
		t.Fatal("обогащение вообще не вызывалось — тест ничего не проверяет")
	}
	if got := res.Connections[0].ClientName; got != "ноутбук" {
		t.Errorf("ClientName=%q, ожидалось \"ноутбук\": сборка пошла на контексте запроса и обеднила снимок", got)
	}
}
