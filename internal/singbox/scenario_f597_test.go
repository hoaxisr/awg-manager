package singbox

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms"
)

// F597 (стенд A9): RCI лежит, после MigrateOff поднят флаг уборки и стоят
// две метки. Тик сторожа каждые 30 с: на тике, где уборка идёт, — одно
// чтение списка на обе уборки; неудача чтения двигает общую выдержку
// 30 с → 1, 2, 4, 8 мин → 15 мин. После восстановления первый же тик уборки
// сносит обе записи одним списком.
func TestScenario_ProxyCleanup_ListDownBackoffOneReadPerTick(t *testing.T) {
	op, w, f, _ := f562Stand(t)
	op.ndmsProxyEnabledFn = func() bool { return false }
	clock := time.Unix(0, 0)
	op.deferredNow = func() time.Time { return clock }
	op.MarkNeedsOrphanCleanup()
	op.deferProxyRemoval("Proxy3", "A")
	op.deferProxyRemoval("Proxy4", "B")
	f.FailList(errors.New("rci down"))

	var at []int
	for sec := 0; sec <= 3600; sec += 30 {
		clock = time.Unix(int64(sec), 0)
		before := f.ListCalls()
		w.tick(context.Background())
		switch n := f.ListCalls() - before; n {
		case 0:
		case 1:
			at = append(at, sec)
		default:
			t.Fatalf("секунда %d: списков за тик %d, want ≤1", sec, n)
		}
	}
	want := []int{0, 30, 90, 210, 450, 930, 1830, 2730}
	if !slices.Equal(at, want) {
		t.Fatalf("чтения списка на секундах %v, want %v", at, want)
	}
	if len(f.Posts) != 0 || !op.needsOrphanCleanup.Load() || len(op.deferredProxies) != 2 {
		t.Fatalf("в простое: posts=%v флаг=%v метки=%v", f.Posts, op.needsOrphanCleanup.Load(), op.deferredProxies)
	}

	f.FailList(nil)
	clock = time.Unix(3630, 0)
	before := f.ListCalls()
	w.tick(context.Background())
	if n := f.ListCalls() - before; n != 1 {
		t.Fatalf("тик восстановления: списков %d, want 1", n)
	}
	if f.Has("Proxy3") || f.Has("Proxy4") || op.needsOrphanCleanup.Load() || len(op.deferredProxies) != 0 {
		t.Fatalf("после восстановления: posts=%v флаг=%v метки=%v", f.Posts, op.needsOrphanCleanup.Load(), op.deferredProxies)
	}
	mustHave(t, f, "Proxy0", "Proxy5", "Proxy6")
	clean(t, f)

	// Успех сбросил выдержку: новая метка добирается на ближайшем тике.
	f.Add(ndms.Interface{ID: "Proxy8", Type: "Proxy", Description: "gone", State: "up"})
	_ = f.DrainHooks()
	op.deferProxyRemoval("Proxy8", "gone")
	clock = time.Unix(3660, 0)
	w.tick(context.Background())
	if f.Has("Proxy8") {
		t.Fatalf("Proxy8 не снят на ближайшем тике: posts=%v", f.Posts)
	}

	// Новый простой начинает выдержку с 30 с, а не с прежних 15 мин.
	f.FailList(errors.New("rci down"))
	op.deferProxyRemoval("Proxy5", "user")
	at = nil
	for sec := 3690; sec <= 3750; sec += 30 {
		clock = time.Unix(int64(sec), 0)
		before := f.ListCalls()
		w.tick(context.Background())
		if f.ListCalls() != before {
			at = append(at, sec)
		}
	}
	if want := []int{3690, 3720}; !slices.Equal(at, want) {
		t.Fatalf("второй простой: чтения на секундах %v, want %v", at, want)
	}
}
