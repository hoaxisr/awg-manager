package linkres

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/proxyrt"
)

func drive(t *testing.T, r proxyrt.Resource) {
	t.Helper()
	for pass := 0; pass < 5; pass++ {
		obs, err := r.Observe(context.Background())
		if err != nil {
			t.Fatalf("observe: %v", err)
		}
		steps := r.Plan(obs)
		if len(steps) == 0 {
			return
		}
		for _, s := range steps {
			if err := r.Apply(context.Background(), s); err != nil {
				t.Fatalf("apply %s: %v", s.Op, err)
			}
		}
	}
	t.Fatal("не сошлось за 5 проходов")
}

type fakeOcc struct {
	taken map[int]bool
	err   error
}

func (f fakeOcc) OccupiedLocalListenPorts(context.Context) (map[int]bool, error) {
	return f.taken, f.err
}

func TestListenPortOK(t *testing.T) {
	lp := NewListenPort("listen_port", fakeOcc{taken: map[int]bool{9001: true}})
	lp.SetDesired("127.0.0.1:9000")
	obs, err := lp.Observe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if steps := lp.Plan(obs); len(steps) != 0 {
		t.Fatalf("свободный порт из пула: %v", steps)
	}
}

func TestListenPortConflictIsVerdict(t *testing.T) {
	// Чинить конфликт ресурс не может: единственный писатель конфига —
	// handler плана 5 (single-writer). Приговор с причиной.
	lp := NewListenPort("listen_port", fakeOcc{taken: map[int]bool{9000: true}})
	lp.SetDesired("127.0.0.1:9000")
	obs, _ := lp.Observe(context.Background())
	steps := lp.Plan(obs)
	if len(steps) != 1 || steps[0].Op != "fail" {
		t.Fatalf("занятый порт — приговор: %v", steps)
	}
	if err := lp.Apply(context.Background(), steps[0]); err == nil ||
		!strings.Contains(err.Error(), "9000") {
		t.Fatalf("причина обязана называть порт: %v", err)
	}
}

func TestListenPortOutOfPoolIsVerdict(t *testing.T) {
	lp := NewListenPort("listen_port", fakeOcc{})
	lp.SetDesired("127.0.0.1:12345")
	obs, _ := lp.Observe(context.Background())
	if steps := lp.Plan(obs); len(steps) != 1 || steps[0].Op != "fail" {
		t.Fatalf("порт вне пула 9000..9200 — приговор: %v", steps)
	}
}

type fakeSync struct {
	tunnels []LinkedTunnel
	synced  []string
	listErr error
}

func (f *fakeSync) List(context.Context, string) ([]LinkedTunnel, error) {
	return f.tunnels, f.listErr
}

func (f *fakeSync) Sync(_ context.Context, clientID, listen string) (int, error) {
	f.synced = append(f.synced, clientID+"→"+listen)
	for i := range f.tunnels {
		f.tunnels[i].Endpoint = "127.0.0.1:9000"
	}
	return len(f.tunnels), nil
}

func TestLinkedEndpointSyncsDrift(t *testing.T) {
	fs := &fakeSync{tunnels: []LinkedTunnel{{ID: "t1", Endpoint: "127.0.0.1:9017"}}}
	le := NewLinkedEndpoint("linked_endpoint", fs)
	le.SetDesired("client1", "127.0.0.1:9000")

	drive(t, le)

	if len(fs.synced) != 1 || fs.synced[0] != "client1→127.0.0.1:9000" {
		t.Fatalf("sync не вызван: %v", fs.synced)
	}
}

func TestLinkedEndpointSettledWithoutTunnels(t *testing.T) {
	// Нет связанных туннелей — нечего доводить, это settled, а не drift.
	le := NewLinkedEndpoint("linked_endpoint", &fakeSync{})
	le.SetDesired("client1", "127.0.0.1:9000")
	obs, _ := le.Observe(context.Background())
	if steps := le.Plan(obs); len(steps) != 0 {
		t.Fatalf("пустой список туннелей: %v", steps)
	}
}

func TestLinkedEndpointListErrorIsUnknown(t *testing.T) {
	le := NewLinkedEndpoint("linked_endpoint", &fakeSync{listErr: errors.New("store")})
	le.SetDesired("client1", "127.0.0.1:9000")
	if _, err := le.Observe(context.Background()); err == nil {
		t.Fatal("ошибка списка обязана быть unknown")
	}
}

type fakeHooks struct{ started, stopped []string }

func (f *fakeHooks) OnTunnelStart(_ context.Context, id, iface string) error {
	f.started = append(f.started, id+"@"+iface)
	return nil
}

func (f *fakeHooks) OnTunnelStop(_ context.Context, id string) error {
	f.stopped = append(f.stopped, id)
	return nil
}

func TestClientRoutesNotifyOnceAndOnChange(t *testing.T) {
	h := &fakeHooks{}
	cr := NewClientRoutes("client_routes", h)
	cr.SetDesired("wdttraw-client1", "opkgtun18", true)
	drive(t, cr)
	drive(t, cr) // повторный прогон — без повторного уведомления
	if len(h.started) != 1 || h.started[0] != "wdttraw-client1@opkgtun18" {
		t.Fatalf("уведомление о старте: %v", h.started)
	}
	// Ренумерация интерфейса — новое уведомление.
	cr.SetDesired("wdttraw-client1", "opkgtun19", true)
	drive(t, cr)
	if len(h.started) != 2 {
		t.Fatalf("смена интерфейса обязана переуведомлять: %v", h.started)
	}
	// Выключение — stop.
	cr.SetDesired("wdttraw-client1", "", false)
	drive(t, cr)
	if len(h.stopped) != 1 {
		t.Fatalf("уведомление об остановке: %v", h.stopped)
	}
}

type fakeRegistry struct{ m map[string]ExitInfo }

func (f *fakeRegistry) Lookup(id string) (ExitInfo, bool) {
	e, ok := f.m[id]
	return e, ok
}

func (f *fakeRegistry) Ensure(e ExitInfo) error {
	f.m[e.ID] = e
	return nil
}

func TestRoutableExitRegistersAtCreationNotAtReady(t *testing.T) {
	// Регистрация — при создании, не при Ready (§5 спеки): иначе правило на
	// выключенный выход становится невидимым вместо «выход недоступен».
	reg := &fakeRegistry{m: map[string]ExitInfo{}}
	re := NewRoutableExit("routable_exit", reg)
	re.SetDesired(ExitInfo{ID: "wdttraw-client1", NDMSName: "OpkgTun18",
		KernelIface: "opkgtun18", Ready: false})
	drive(t, re)
	got, ok := reg.m["wdttraw-client1"]
	if !ok || got.Ready {
		t.Fatalf("выход обязан быть зарегистрирован с Ready=false: %+v, %v", got, ok)
	}
	// Готовность меняет запись.
	re.SetDesired(ExitInfo{ID: "wdttraw-client1", NDMSName: "OpkgTun18",
		KernelIface: "opkgtun18", Ready: true})
	drive(t, re)
	if !reg.m["wdttraw-client1"].Ready {
		t.Fatal("Ready не доехал до реестра")
	}
}

// roleExitFlip — роль-двойник с желаемым, зависящим от наблюдений (у
// raw-клиента так считается Ready). Движок зовёт Resources ДВАЖДЫ за проход и
// планирует по ВТОРОМУ списку: первый нужен только чтобы было что наблюдать.
type roleExitFlip struct {
	re     *RoutableExit
	calls  int
	first  ExitInfo
	second ExitInfo
}

func (r *roleExitFlip) Resources(proxyrt.Intent, any, proxyrt.Observations) []proxyrt.Resource {
	info := r.second
	if r.calls == 0 {
		info = r.first
	}
	r.calls++
	r.re.SetDesired(info)
	return []proxyrt.Resource{r.re}
}

func TestRoutableExitPlansAgainstFreshDesired(t *testing.T) {
	// I2 ревью: сравнение желаемого, запечённое в Observe, судит по желаемому
	// ПЕРВОГО вызова Resources — запись реестра совпала со старым want, шага
	// нет, и новое желаемое уезжает в реестр лишь следующей реконсиляцией.
	stale := ExitInfo{ID: "wdttraw-client1", NDMSName: "OpkgTun18",
		KernelIface: "opkgtun18", Ready: false}
	fresh := stale
	fresh.Ready = true
	reg := &fakeRegistry{m: map[string]ExitInfo{stale.ID: stale}}
	role := &roleExitFlip{re: NewRoutableExit("routable_exit", reg), first: stale, second: fresh}

	res, phase := proxyrt.NewReconciler(role, nil, proxyrt.ReconcileOpts{}).
		Run(context.Background(), proxyrt.IntentEnabled)

	if got := reg.m[stale.ID]; !got.Ready {
		t.Fatalf("публикация обязана отражать желаемое второго вызова Resources в том же прогоне: %+v (фаза %v, шаги %v)",
			got, phase, res.Steps)
	}
}
