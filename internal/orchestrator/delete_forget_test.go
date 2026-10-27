package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel/wan"
)

// forgetRecorder — исполнитель маршрутов всех трёх видов: пишет, что звали.
type forgetRecorder struct {
	name  string
	calls *[]string
}

func (r forgetRecorder) rec(what string) error {
	*r.calls = append(*r.calls, r.name+":"+what)
	return nil
}
func (r forgetRecorder) Reconcile(context.Context) error              { return r.rec("reconcile") }
func (r forgetRecorder) OnTunnelStop(context.Context, string) error   { return nil }
func (r forgetRecorder) OnTunnelDelete(context.Context, string) error { return nil }
func (r forgetRecorder) ForgetTunnel(_ context.Context, id string) error {
	return r.rec("forget " + id)
}

type staticForget struct{ forgetRecorder }

func (r staticForget) OnTunnelStart(context.Context, string, string) error { return nil }

type clientForget struct{ forgetRecorder }

func (r clientForget) OnTunnelStart(context.Context, string, string) error { return nil }

// F609: правила туннеля уходят из хранилищ только после успешного удаления.
// Отказ удаления (список интерфейсов не прочитан) оставляет их целыми и
// пересверяет DNS- и статические маршруты, чьи строки OnTunnelDelete уже снял.
func TestExecuteDeleteKernel_ForgetsRoutesOnlyAfterSuccess(t *testing.T) {
	store := lifecycleStore(t, &storage.AWGTunnel{ID: "awg10", Name: "g"})
	op := &fakeKernelOp{deleteErr: errors.New("read OpkgTun record: rci down")}
	var calls []string
	o := &Orchestrator{state: newState(), store: store, kernelOp: op, wanModel: wan.NewModel(),
		dnsRoute:    forgetRecorder{"dns", &calls},
		staticRoute: staticForget{forgetRecorder{"static", &calls}},
		clientRoute: clientForget{forgetRecorder{"client", &calls}},
	}

	if err := o.executeOne(context.Background(), Action{Type: ActionDeleteKernel, Tunnel: "awg10"}); err == nil {
		t.Fatal("отказ Delete обязан вернуться")
	}
	if got := fmt.Sprint(calls); got != "[dns:reconcile static:reconcile]" {
		t.Fatalf("после отказа удаления: %s, ожидалась только пересверка без забывания", got)
	}

	calls = nil
	op.deleteErr = nil
	if err := o.executeOne(context.Background(), Action{Type: ActionDeleteKernel, Tunnel: "awg10"}); err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(calls); got != "[dns:forget awg10 static:forget awg10 client:forget awg10]" {
		t.Fatalf("после удаления: %s", got)
	}
	if store.Exists("awg10") {
		t.Fatal("запись жива после успешного Delete")
	}
}
