package transport

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// stuckWire is a carrier that takes nothing, like a Direct listener whose queue is full because no client
// ever connected to it.
type stuckWire struct{ negotiationWire }

func (w *stuckWire) Send([]byte) error { return errors.New("direct: queue full") }

// An exit that also listens on a carrier nobody uses must keep serving the one its client does use. The
// unused carrier's queue fills with the exit's hellos and pings; the exit's receive path used to block on
// it for good (offering a challenge to a new client sends to every link), after which no client could
// shake hands: the carrier delivered its hello, the exit never read another record.
func TestSessionServesNewClientsWhileALinkIsStuck(t *testing.T) {
	client, exit, cw, _ := linkedSessions(t, "script")
	if err := exit.AddTransport("direct", &stuckWire{}, testSessionSecret, testSessionCtx, 110); err != nil {
		t.Fatal(err)
	}
	var got atomic.Int32
	exit.Receive(func([]byte) { got.Add(1) })
	startPair(t, client, exit)

	// Fill the stuck link the way hours of idle keepalives do.
	filled := make(chan struct{})
	go func() {
		defer close(filled)
		for i := 0; i < 3*batchQueueDepth; i++ {
			_ = exit.helloVia("direct")
		}
	}()
	select {
	case <-filled:
	case <-time.After(2 * time.Second):
		t.Error("a hello for the stuck link blocked its sender")
	}

	// The client restarts: the exit offers the new one a challenge on every link.
	_ = client.Stop()
	restarted := restartOn(t, false, cw["script"])
	started := make(chan error, 1)
	go func() { started <- restarted.Start() }()
	select {
	case err := <-started:
		if err != nil {
			t.Fatalf("the exit did not accept the restarted client: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the exit's receive path is blocked on the stuck link")
	}
	eventually(t, "data from the restarted client", func() bool {
		_ = restarted.Send(testIPv4(40, 6))
		return got.Load() > 0
	})
}
