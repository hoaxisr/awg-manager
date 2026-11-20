package serveringress

import (
	"context"
	"errors"
	"testing"
)

type mockBootLifecycle struct {
	running         bool
	startErr        error
	shutdownErr     error
	startCalls      int
	shutdownCalls   int
	shutdownCtxDone bool
}

func (m *mockBootLifecycle) StartConfigured() error {
	m.startCalls++
	if m.startErr != nil {
		return m.startErr
	}
	m.running = true
	return nil
}

func (m *mockBootLifecycle) ShutdownRuntime(ctx context.Context) error {
	m.shutdownCalls++
	if ctx != nil && ctx.Err() != nil {
		m.shutdownCtxDone = true
	}
	m.running = false
	return m.shutdownErr
}

func (m *mockBootLifecycle) IsRunning() bool {
	return m.running
}

func TestStartIngressIfSafe_RecoveryGatesBlock(t *testing.T) {
	xray := &mockBootLifecycle{}
	tg := &mockBootLifecycle{}
	disp := &mockBootLifecycle{}

	// 1. Coordinator recovery gate blocks
	started, err := StartIngressIfSafe(
		context.Background(),
		true, "corrupt journal",
		false, "",
		false, "",
		true, xray,
		true, tg,
		disp,
	)
	if started || err == nil {
		t.Fatalf("expected coordinator gate to block, started=%v, err=%v", started, err)
	}
	if xray.startCalls > 0 || tg.startCalls > 0 || disp.startCalls > 0 {
		t.Fatalf("expected 0 start calls, got xray=%d, tg=%d, disp=%d", xray.startCalls, tg.startCalls, disp.startCalls)
	}

	// 2. Xray local recovery gate blocks
	started, err = StartIngressIfSafe(
		context.Background(),
		false, "",
		true, "xray settings corrupt",
		false, "",
		true, xray,
		true, tg,
		disp,
	)
	if started || err == nil {
		t.Fatalf("expected xray gate to block, started=%v, err=%v", started, err)
	}

	// 3. TG local recovery gate blocks
	started, err = StartIngressIfSafe(
		context.Background(),
		false, "",
		false, "",
		true, "tproxy corrupt",
		true, xray,
		true, tg,
		disp,
	)
	if started || err == nil {
		t.Fatalf("expected tg gate to block, started=%v, err=%v", started, err)
	}
}

func TestStartIngressIfSafe_NilLifecycleFailsClosed(t *testing.T) {
	tg := &mockBootLifecycle{}
	disp := &mockBootLifecycle{}

	// Xray enabled but nil
	started, err := StartIngressIfSafe(
		context.Background(),
		false, "", false, "", false, "",
		true, nil,
		true, tg,
		disp,
	)
	if started || err == nil {
		t.Fatalf("expected nil xray to fail, started=%v, err=%v", started, err)
	}

	// TG enabled but nil
	xray := &mockBootLifecycle{}
	started, err = StartIngressIfSafe(
		context.Background(),
		false, "", false, "", false, "",
		true, xray,
		true, nil,
		disp,
	)
	if started || err == nil {
		t.Fatalf("expected nil tg to fail, started=%v, err=%v", started, err)
	}

	// Ingress enabled but dispatcher nil
	started, err = StartIngressIfSafe(
		context.Background(),
		false, "", false, "", false, "",
		true, xray,
		false, tg,
		nil,
	)
	if started || err == nil {
		t.Fatalf("expected nil dispatcher to fail, started=%v, err=%v", started, err)
	}
}

func TestStartIngressIfSafe_SuccessPath(t *testing.T) {
	xray := &mockBootLifecycle{}
	tg := &mockBootLifecycle{}
	disp := &mockBootLifecycle{}

	started, err := StartIngressIfSafe(
		context.Background(),
		false, "", false, "", false, "",
		true, xray,
		true, tg,
		disp,
	)
	if !started || err != nil {
		t.Fatalf("expected clean start, started=%v, err=%v", started, err)
	}

	if xray.startCalls != 1 || !xray.running {
		t.Fatalf("expected xray started once, got calls=%d, running=%v", xray.startCalls, xray.running)
	}
	if tg.startCalls != 1 || !tg.running {
		t.Fatalf("expected tg started once, got calls=%d, running=%v", tg.startCalls, tg.running)
	}
	if disp.startCalls != 1 || !disp.running {
		t.Fatalf("expected disp started once, got calls=%d, running=%v", disp.startCalls, disp.running)
	}
}

func TestStartIngressIfSafe_CompensationOnTgFailure(t *testing.T) {
	xray := &mockBootLifecycle{}
	tg := &mockBootLifecycle{startErr: errors.New("tg failed to bind 8085")}
	disp := &mockBootLifecycle{}

	started, err := StartIngressIfSafe(
		context.Background(),
		false, "", false, "", false, "",
		true, xray,
		true, tg,
		disp,
	)
	if started || err == nil {
		t.Fatalf("expected start failure, got started=%v, err=%v", started, err)
	}

	// Xray was started, but compensated and shutdown
	if xray.startCalls != 1 {
		t.Fatalf("expected xray start call, got %d", xray.startCalls)
	}
	if xray.shutdownCalls != 1 || xray.running {
		t.Fatalf("expected xray shutdown call and stopped, got calls=%d, running=%v", xray.shutdownCalls, xray.running)
	}
	// Dispatcher was never started
	if disp.startCalls != 0 {
		t.Fatalf("expected dispatcher not started, got %d", disp.startCalls)
	}
}

func TestStartIngressIfSafe_CompensationOnDispatcherFailure(t *testing.T) {
	xray := &mockBootLifecycle{}
	tg := &mockBootLifecycle{}
	disp := &mockBootLifecycle{startErr: errors.New("dispatcher bind 9009 failed")}

	started, err := StartIngressIfSafe(
		context.Background(),
		false, "", false, "", false, "",
		true, xray,
		true, tg,
		disp,
	)
	if started || err == nil {
		t.Fatalf("expected start failure, got started=%v, err=%v", started, err)
	}

	// Both TG and Xray were started, then compensated
	if tg.shutdownCalls != 1 || tg.running {
		t.Fatalf("expected tg compensated, calls=%d, running=%v", tg.shutdownCalls, tg.running)
	}
	if xray.shutdownCalls != 1 || xray.running {
		t.Fatalf("expected xray compensated, calls=%d, running=%v", xray.shutdownCalls, xray.running)
	}
}

func TestStartIngressIfSafe_PreexistingRunningServiceNotCompensated(t *testing.T) {
	xray := &mockBootLifecycle{running: true} // Already running prior to helper!
	tg := &mockBootLifecycle{startErr: errors.New("tg bind failed")}
	disp := &mockBootLifecycle{}

	started, err := StartIngressIfSafe(
		context.Background(),
		false, "", false, "", false, "",
		true, xray,
		true, tg,
		disp,
	)
	if started || err == nil {
		t.Fatalf("expected failure, got started=%v", started)
	}

	// Xray was already running, helper did not start it, so helper MUST NOT shut it down!
	if xray.startCalls != 0 {
		t.Fatalf("expected 0 start calls for already-running xray, got %d", xray.startCalls)
	}
	if xray.shutdownCalls != 0 || !xray.running {
		t.Fatalf("expected pre-existing xray NOT compensated, got shutdownCalls=%d, running=%v", xray.shutdownCalls, xray.running)
	}
}

func TestStartIngressIfSafe_CompensationWithCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel context before call!

	xray := &mockBootLifecycle{}
	tg := &mockBootLifecycle{startErr: errors.New("tg startup failure")}
	disp := &mockBootLifecycle{}

	started, err := StartIngressIfSafe(
		ctx,
		false, "", false, "", false, "",
		true, xray,
		true, tg,
		disp,
	)
	if started || err == nil {
		t.Fatalf("expected failure, got started=%v", started)
	}

	// Compensation should have succeeded with its independent context, not parent canceled context
	if xray.shutdownCalls != 1 || xray.shutdownCtxDone {
		t.Fatalf("expected compensation to run with active cleanup context, got calls=%d, ctxDone=%v", xray.shutdownCalls, xray.shutdownCtxDone)
	}
}
