package orchestrator

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel/wan"
)

func newResumeOrch(t *testing.T, fake *fakeKernelOp, stored *storage.AWGTunnel) *Orchestrator {
	t.Helper()
	dir := t.TempDir()
	store := storage.NewAWGTunnelStoreWithLockDir(dir, filepath.Join(dir, "locks"))
	if stored != nil {
		if err := store.Create(stored); err != nil {
			t.Fatal(err)
		}
	}
	return &Orchestrator{
		state:    newState(),
		store:    store,
		kernelOp: fake,
		wanModel: wan.NewModel(),
		appLog:   logging.NewScopedLogger(&capturingLog{}, logging.GroupTunnel, logging.SubOrchestrator),
	}
}

// F216: Resume — это ровно `ip link set up`. Выдаётся он по WAN-up туннелю с
// явной привязкой, то есть в момент смены канала: хост-маршрут до endpoint
// остался смотреть на прежний шлюз. Возврат линка обязан привести и маршрут.
func TestResumeKernel_RefreshesEndpointRoute(t *testing.T) {
	fake := &fakeKernelOp{}
	o := newResumeOrch(t, fake, &storage.AWGTunnel{
		ID:           "awg1",
		Name:         "bound",
		ISPInterface: "eth3",
		Peer:         storage.AWGPeer{Endpoint: "vpn.example.com:51820"},
	})

	if err := o.executeResumeKernel(context.Background(), Action{Type: ActionResumeKernel, Tunnel: "awg1"}); err != nil {
		t.Fatalf("executeResumeKernel: %v", err)
	}

	if fake.resumes.Load() != 1 {
		t.Errorf("Resume вызван %d раз", fake.resumes.Load())
	}
	calls := fake.routeCalls()
	if len(calls) != 1 {
		t.Fatalf("маршрут до endpoint не обновлён: вызовов %d", len(calls))
	}
	got := calls[0]
	if got.tunnelID != "awg1" || got.endpoint != "vpn.example.com:51820" {
		t.Errorf("вызов с %+v", got)
	}
	if got.kernelDevice != "eth3" || got.ispName != "eth3" {
		t.Errorf("маршрут кладётся мимо привязанного канала: %+v", got)
	}
}

// Без endpoint приводить нечего — лишний вызов означал бы резолв пустой строки.
func TestResumeKernel_SkipsWhenNoEndpoint(t *testing.T) {
	fake := &fakeKernelOp{}
	o := newResumeOrch(t, fake, &storage.AWGTunnel{ID: "awg1", Name: "no-endpoint", ISPInterface: "eth3"})

	if err := o.executeResumeKernel(context.Background(), Action{Type: ActionResumeKernel, Tunnel: "awg1"}); err != nil {
		t.Fatal(err)
	}
	if n := len(fake.routeCalls()); n != 0 {
		t.Errorf("без endpoint маршрут трогать нечего, а вызовов %d", n)
	}
}

// Отказ маршрута не роняет возврат линка: туннель уже поднят, и на
// одноканальном роутере этот маршрут для внешнего трафика избыточен.
func TestResumeKernel_EndpointRouteFailureIsNotFatal(t *testing.T) {
	fake := &fakeKernelOp{endpointErr: context.DeadlineExceeded}
	o := newResumeOrch(t, fake, &storage.AWGTunnel{
		ID:           "awg1",
		Name:         "bound",
		ISPInterface: "eth3",
		Peer:         storage.AWGPeer{Endpoint: "1.2.3.4:51820"},
	})

	if err := o.executeResumeKernel(context.Background(), Action{Type: ActionResumeKernel, Tunnel: "awg1"}); err != nil {
		t.Errorf("отказ маршрута не должен ронять Resume: %v", err)
	}
	if fake.resumes.Load() != 1 {
		t.Errorf("Resume вызван %d раз", fake.resumes.Load())
	}
}

// Свежий адрес endpoint обязан осесть в записи — как после старта и
// реконсайла. Иначе следующий холодный старт засеет маршрут протухшим IP из
// стора и туннель пойдёт через мёртвый шлюз.
func TestResumeKernel_PersistsResolvedEndpoint(t *testing.T) {
	fake := &fakeKernelOp{endpointIP: "203.0.113.55"}
	o := newResumeOrch(t, fake, &storage.AWGTunnel{
		ID:                 "awg1",
		Name:               "bound",
		ISPInterface:       "eth3",
		ResolvedEndpointIP: "198.51.100.1", // протухший адрес прошлого сеанса
		Peer:               storage.AWGPeer{Endpoint: "vpn.example.com:51820"},
	})

	if err := o.executeResumeKernel(context.Background(), Action{Type: ActionResumeKernel, Tunnel: "awg1"}); err != nil {
		t.Fatalf("executeResumeKernel: %v", err)
	}

	stored, err := o.store.Get("awg1")
	if err != nil {
		t.Fatal(err)
	}
	if stored.ResolvedEndpointIP != "203.0.113.55" {
		t.Errorf("ResolvedEndpointIP = %q, ожидали свежий 203.0.113.55", stored.ResolvedEndpointIP)
	}
	if stored.ActiveWAN != "eth3" {
		t.Errorf("ActiveWAN = %q, ожидали eth3", stored.ActiveWAN)
	}
}
