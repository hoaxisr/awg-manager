package orchestrator

import (
	"context"
	"testing"
	"time"
)

// running nativewg tunnel whose boot-quiescence window has long elapsed —
// the state in which a conf=disabled edge is taken as user intent to stop.
func settledTunnel() *Orchestrator {
	o := &Orchestrator{state: newState(), confSettleDelay: 50 * time.Millisecond}
	o.state.tunnels["awg11"] = &tunnelState{
		ID: "awg11", Backend: "nativewg", NWGIndex: 2,
		Enabled: true, Running: true,
		quiescentUntil: time.Now().Add(-time.Hour),
	}
	return o
}

func confHook(level string) Event {
	return Event{Type: EventNDMSHook, NDMSName: "Wireguard2", Layer: "conf", Level: level}
}

// Issue #667: NDMS ping-check restarts the interface by bouncing conf
// disabled→running within a couple of seconds. Taking the disabled edge at
// face value stopped the tunnel and tore down its routes.
func TestConfDisabled_SuppressedWhenNDMSBouncesBackToRunning(t *testing.T) {
	o := settledTunnel()

	go func() {
		time.Sleep(10 * time.Millisecond)
		_ = o.HandleEvent(context.Background(), confHook("running"))
	}()

	if o.settleConfDisabled(context.Background(), confHook("disabled")) {
		t.Fatal("conf=disabled followed by conf=running is an NDMS restart — the stop must be suppressed")
	}
}

// A genuine disable from the router web UI has no conf=running behind it and
// must still stop the tunnel (issue #183 sync intent).
func TestConfDisabled_StopsWhenNoRunningFollows(t *testing.T) {
	o := settledTunnel()

	if !o.settleConfDisabled(context.Background(), confHook("disabled")) {
		t.Fatal("a lone conf=disabled is user intent — the stop must proceed")
	}
}

// A conf=running seen BEFORE the disabled edge (e.g. the tunnel's own start)
// must not cancel a later, unrelated disable.
func TestConfDisabled_StaleRunningDoesNotSuppress(t *testing.T) {
	o := settledTunnel()
	_ = o.HandleEvent(context.Background(), confHook("running"))

	if !o.settleConfDisabled(context.Background(), confHook("disabled")) {
		t.Fatal("a conf=running that predates the disabled edge must not suppress the stop")
	}
}

// Inside the boot-quiescence window decide() already suppresses; settle must
// not add its own delay there.
func TestConfDisabled_NoWaitInsideQuiescence(t *testing.T) {
	o := settledTunnel()
	o.state.tunnels["awg11"].quiescentUntil = time.Now().Add(time.Minute)
	o.confSettleDelay = 5 * time.Second

	start := time.Now()
	o.settleConfDisabled(context.Background(), confHook("disabled"))

	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("settle must not wait inside the quiescence window, waited %s", elapsed)
	}
}
