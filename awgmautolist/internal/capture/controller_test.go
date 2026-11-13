package capture

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakePacketSocket struct {
	closed atomic.Bool
}

func (s *fakePacketSocket) Read([]byte) (int, error) {
	if s.closed.Load() {
		return 0, context.Canceled
	}
	time.Sleep(10 * time.Millisecond)
	return 0, errReadTimeout
}

func (s *fakePacketSocket) Close() error {
	s.closed.Store(true)
	return nil
}

func TestControllerPauseResumeClosesSocketGeneration(t *testing.T) {
	var mu sync.Mutex
	var sockets []*fakePacketSocket
	controller := NewController([]string{"br0", "br1"}, true, nil, nil)
	controller.open = func(string) (packetSocket, error) {
		socket := &fakePacketSocket{}
		mu.Lock()
		sockets = append(sockets, socket)
		mu.Unlock()
		return socket, nil
	}
	if err := controller.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !controller.Status().Running {
		t.Fatal("capture did not start")
	}
	if err := controller.Pause(); err != nil {
		t.Fatal(err)
	}
	if controller.Status().Running || controller.Status().FlowStats.Flows != 0 {
		t.Fatalf("capture did not pause cleanly: %+v", controller.Status())
	}
	if err := controller.Resume(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := controller.Stop(); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(sockets) != 4 {
		t.Fatalf("opened %d sockets, want two per generation", len(sockets))
	}
	for i, socket := range sockets {
		if !socket.closed.Load() {
			t.Fatalf("socket %d was not closed", i)
		}
	}
}

func TestControllerStopIsIdempotent(t *testing.T) {
	controller := NewController([]string{"br0"}, false, nil, nil)
	controller.open = func(string) (packetSocket, error) { return &fakePacketSocket{}, nil }
	if err := controller.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := controller.Stop(); err != nil {
		t.Fatal(err)
	}
	if err := controller.Stop(); err != nil {
		t.Fatal(err)
	}
}

func TestControllerParentCancellation(t *testing.T) {
	controller := NewController([]string{"br0"}, false, nil, nil)
	controller.open = func(string) (packetSocket, error) { return &fakePacketSocket{}, nil }
	ctx, cancel := context.WithCancel(context.Background())
	if err := controller.Start(ctx); err != nil {
		t.Fatal(err)
	}
	cancel()
	deadline := time.Now().Add(2 * time.Second)
	for controller.Status().Running && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if controller.Status().Running {
		t.Fatal("capture remained running after parent cancellation")
	}
	if err := controller.Stop(); err != nil {
		t.Fatal(err)
	}
}
