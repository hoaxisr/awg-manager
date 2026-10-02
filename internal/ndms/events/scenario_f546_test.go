package events

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/ndms/query"
)

// Сценарии Фазы 1 F546 на оракуле: E считает то, что на живом роутере
// становится строкой E в журнале ndm.

// deliverHooks отдаёт накопленные оракулом хуки диспетчеру одной пачкой и
// ждёт её конца: списка, если в пачке были хуки существования, иначе прохода.
func deliverHooks(t *testing.T, f *query.FakeNDMS, q *query.Queries) {
	t.Helper()
	d := NewDispatcher(q, NopLogger())
	done := drainBarrier(d)
	listed := listedBarrier(d)
	existence := false
	for _, h := range f.DrainHooks() {
		existence = existence || h.Type == string(EventIfCreated) || h.Type == string(EventIfDestroyed)
		d.Enqueue(Event{Type: EventType(h.Type), ID: h.ID, Layer: h.Layer, Level: h.Level})
	}
	d.Start()
	defer d.Stop()
	if existence {
		waitListed(t, listed)
		return
	}
	waitDrain(t, done)
}

// Чужой поток (как на HEAD) шлёт `interface X down` по отсутствующему X и
// сразу `no interface X`; хуки обоих событий приходят одной пачкой.
func TestScenario_PhantomPairFromOwnCommand(t *testing.T) {
	ctx := context.Background()
	f := query.NewFakeNDMS(ndms.Interface{ID: "Bridge0", Type: "Bridge"})
	q := oracleQueries(t, f)
	_, _ = q.Interfaces.List(ctx) // bootstrap
	lists := f.ListCalls()

	for _, line := range []string{"interface Wireguard5 down", "no interface Wireguard5"} {
		if _, err := f.Post(ctx, map[string]any{"parse": line}); err != nil {
			t.Fatalf("post %q: %v", line, err)
		}
	}
	deliverHooks(t, f, q)

	// Phantoms == 1 здесь ожидаем: команда по отсутствующему пришла снаружи
	// теста, Фаза 1 её не убирает (это Фаза 2). Фаза 1 убирает E по паре
	// created→destroyed — поэтому ассерт только на E и на цену пачки: ifcreated
	// незнакомого id расходится с картой — один список на пачку (дизайн §5).
	if f.E != 0 {
		t.Fatalf("E=%d, want 0: пара created→destroyed не должна читать по имени", f.E)
	}
	if got := f.ListCalls() - lists; got != 1 {
		t.Fatalf("пара created→destroyed стоила %d списков, want 1", got)
	}
	if got, _ := q.Interfaces.Get(ctx, "Wireguard5"); got != nil {
		t.Fatalf("снятый X остался в кэше: %#v", got)
	}
}

// ifdestroyed потерян: поллер пиров трижды спрашивает X. Пиры — из снимка
// списка: ни одного чтения по имени, E нет вовсе (F546); X уходит из карты со
// следующим свежим списком.
func TestScenario_LostDestroyThenPoll(t *testing.T) {
	ctx := context.Background()
	f := query.NewFakeNDMS(ndms.Interface{ID: "Wireguard3", Type: "Wireguard"})
	q := oracleQueries(t, f)
	_, _ = q.Interfaces.List(ctx) // bootstrap: X в кэше
	f.Remove("Wireguard3")
	_ = f.DrainHooks() // хук потерян

	for i := 0; i < 3; i++ {
		peers, err := q.Peers.GetPeers(ctx, "Wireguard3")
		if err != nil || len(peers) != 0 {
			t.Fatalf("poll %d: peers=%v err=%v, want пусто без ошибки", i, peers, err)
		}
		q.Peers.Invalidate("Wireguard3") // истёк TTL пиров — следующий опрос идёт в fetch
	}
	if f.E != 0 || len(f.Posts) != 0 {
		t.Fatalf("E=%d Posts=%v, want 0/none: опрос по снятому X не спрашивает по имени", f.E, f.Posts)
	}
	q.Interfaces.Invalidate("Wireguard3") // наша запись — снимок читает свежий список
	if _, err := q.Peers.GetPeers(ctx, "Wireguard3"); err != nil {
		t.Fatal(err)
	}
	if got, _ := q.Interfaces.Get(ctx, "Wireguard3"); got != nil {
		t.Fatalf("X не выселен свежим списком: %#v", got)
	}
	if f.E != 0 {
		t.Fatalf("E=%d, want 0", f.E)
	}
}

// Список (InvalidateAll) уже увидел Y, хук ifcreated Y доехал позже: id
// известен — ни одного лишнего списка.
func TestScenario_DelayedHooksVsInvalidateAll(t *testing.T) {
	ctx := context.Background()
	f := query.NewFakeNDMS(ndms.Interface{ID: "Bridge0", Type: "Bridge"})
	q := oracleQueries(t, f)
	_, _ = q.Interfaces.List(ctx) // bootstrap
	f.Add(ndms.Interface{ID: "Wireguard4", Type: "Wireguard", SystemName: "nwg4"})
	q.Interfaces.InvalidateAll()
	if got, _ := q.Interfaces.Get(ctx, "Wireguard4"); got == nil {
		t.Fatal("InvalidateAll не увидел Y")
	}
	lists, posts := f.ListCalls(), len(f.Posts)

	deliverHooks(t, f, q)

	// Точечное чтение живого Y E не даёт — ловится только счётом POST.
	if got := f.ListCalls() - lists; got != 0 || len(f.Posts) != posts || f.E != 0 {
		t.Fatalf("поздний хук известного Y: %d списков, %d POST, E=%d; want 0, 0, 0", got, len(f.Posts)-posts, f.E)
	}
	if got, _ := q.Interfaces.Get(ctx, "Wireguard4"); got == nil || got.SystemName != "nwg4" {
		t.Fatalf("Y потерян или испорчен после позднего хука: %#v", got)
	}
}

type recLogger struct {
	mu   sync.Mutex
	msgs []string
}

func (l *recLogger) Warnf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.msgs = append(l.msgs, fmt.Sprintf(format, args...))
}

func (l *recLogger) has(sub string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, m := range l.msgs {
		if strings.Contains(m, sub) {
			return true
		}
	}
	return false
}

// Список пачки не прочитался: Warn, метка «грязно» остаётся — следующее чтение
// карты добирает Z одним списком.
func TestScenario_ListErrorAfterHooks_DirtyKept(t *testing.T) {
	ctx := context.Background()
	f := query.NewFakeNDMS(ndms.Interface{ID: "Bridge0", Type: "Bridge"})
	q := oracleQueries(t, f)
	_, _ = q.Interfaces.List(ctx) // bootstrap
	lists := f.ListCalls()

	f.FailList(errors.New("rci timeout"))
	f.Add(ndms.Interface{ID: "Wireguard6", Type: "Wireguard", SystemName: "nwg6"})
	log := &recLogger{}
	d := NewDispatcher(q, log)
	listed := listedBarrier(d)
	for _, h := range f.DrainHooks() {
		d.Enqueue(Event{Type: EventType(h.Type), ID: h.ID, Layer: h.Layer, Level: h.Level})
	}
	d.Start()
	defer d.Stop()
	waitListed(t, listed)

	if !log.has("reconcile dirty") {
		t.Fatalf("ошибка ReconcileDirty не дошла до журнала: %v", log.msgs)
	}
	if got := f.ListCalls() - lists; got != 1 {
		t.Fatalf("первый проход: %d списков, want 1 (неудачный)", got)
	}

	// Метка осталась: чтение карты читает список.
	f.FailList(nil)
	if got, _ := q.Interfaces.Get(ctx, "Wireguard6"); got == nil || got.SystemName != "nwg6" {
		t.Fatalf("Z не добран из списка: %#v", got)
	}
	if got := f.ListCalls() - lists; got != 2 || f.E != 0 {
		t.Fatalf("всего %d списков, E=%d; want 2, 0", got, f.E)
	}
}

// S5 (F570, стенд 30.09): импорт NativeWG, внешнее снятие в первые секунды,
// ifdestroyed ещё не доехал — панель на /system-tunnels спрашивает имя ядра.
// Резолвер по имени дал бы E `Base: unable to find`; имя Wireguard — из
// таблицы классов, без RCI.
func TestScenario_ResolveAfterExternalRemoval(t *testing.T) {
	ctx := context.Background()
	f := query.NewFakeNDMS(ndms.Interface{ID: "Bridge0", Type: "Bridge"})
	q := oracleQueries(t, f)
	_, _ = q.Interfaces.List(ctx) // bootstrap
	if _, err := f.Post(ctx, map[string]any{"interface": map[string]any{"wireguard": map[string]any{"import": "conf"}}}); err != nil {
		t.Fatal(err)
	}
	if _, _, ok, err := q.Interfaces.Confirm(ctx, "Wireguard0"); err != nil || !ok {
		t.Fatalf("Confirm импортированного: ok=%v err=%v", ok, err)
	}
	f.Remove("Wireguard0")
	_ = f.DrainHooks() // хуки задержаны очередью NDMS
	posts := len(f.Posts)

	_, _ = q.WGServers.ListSystemTunnels(ctx)
	_ = q.Interfaces.ResolveSystemName(ctx, "Wireguard0")
	_ = q.Interfaces.SystemNames(ctx, []string{"Wireguard0"})

	if f.E != 0 {
		t.Fatalf("E=%d, want 0: имя снятого X спрошено по имени", f.E)
	}
	for _, p := range f.Posts[posts:] {
		if strings.Contains(p, "system-name") {
			t.Fatalf("резолвер по требованию: %s", p)
		}
	}
}

// system_name из хука ложится в карту имён до разбора типа события: имя
// недетерминированного класса известно без резолвера.
func TestScenario_HookSystemName(t *testing.T) {
	ctx := context.Background()
	f := query.NewFakeNDMS(ndms.Interface{ID: "UsbQmi0", Type: "UsbQmi", SecurityLevel: "public"})
	q := oracleQueries(t, f)
	d := NewDispatcher(q, NopLogger())
	done := drainBarrier(d)
	d.Enqueue(Event{Type: EventIfLayerChanged, ID: "UsbQmi0", Layer: "ctrl", Level: "running", SystemName: "usb0"})
	d.Start()
	defer d.Stop()
	waitDrain(t, done)

	if got := q.Interfaces.SystemNames(ctx, []string{"UsbQmi0"}); got["UsbQmi0"] != "usb0" {
		t.Fatalf("SystemNames = %v, want usb0 из хука", got)
	}
	for _, p := range f.Posts {
		if strings.Contains(p, "system-name") {
			t.Fatalf("резолвер при имени из хука: %s", p)
		}
	}
}
