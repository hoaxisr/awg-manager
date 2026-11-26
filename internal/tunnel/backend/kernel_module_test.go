package backend

import (
	"context"
	"errors"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/netdev"
)

func stubModuleLoaded(t *testing.T, v bool) {
	t.Helper()
	prev := moduleLoaded
	moduleLoaded = func() bool { return v }
	t.Cleanup(func() { moduleLoaded = prev })
}

// Модуль не загружен и не грузится: Start и Recreate отказывают с причиной
// до сноса старого устройства (tun от NDMS остаётся на месте).
func TestKernelBackend_ModuleUnavailable_NoDeviceTouched(t *testing.T) {
	stubModuleLoaded(t, false)
	errMod := errors.New("собран для ядра 4.9-ndm-5, а на роутере ядро 4.9-ndm-6")
	for _, op := range []string{"start", "recreate"} {
		t.Run(op, func(t *testing.T) {
			r := newSwapRig(t, true, nil)
			b := NewKernel(&netdev.SwapGate{})
			calls := 0
			b.SetModuleEnsure(func(context.Context) error { calls++; return errMod })
			var err error
			if op == "start" {
				err = b.Start(context.Background(), "opkgtun10")
			} else {
				err = b.Recreate(context.Background(), "opkgtun10")
			}
			if !errors.Is(err, errMod) {
				t.Fatalf("want module error, got %v", err)
			}
			if calls != 1 {
				t.Fatalf("ensure called %d times", calls)
			}
			if got := r.links(); len(got) != 0 {
				t.Fatalf("devices touched without a module: %v", got)
			}
		})
	}
}

// Модуль подгрузился по требованию (F923): старт идёт дальше.
func TestKernelBackend_ModuleLoadedOnDemand(t *testing.T) {
	stubModuleLoaded(t, false)
	r := newSwapRig(t, false, nil)
	b := NewKernel(&netdev.SwapGate{})
	b.SetModuleEnsure(func(context.Context) error { return nil })
	if err := b.Start(context.Background(), "opkgtun10"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if len(r.links()) == 0 {
		t.Fatal("device was not created after the module loaded")
	}
}
