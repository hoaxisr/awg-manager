package events

import (
	"context"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms/query"
)

const ifaceListPath = "/show/interface/"

const sampleList = `{"Wireguard0": {"id":"Wireguard0","type":"Wireguard","state":"up"}}`

func primedQueries(_ *testing.T) (*query.Queries, *query.FakeGetter) {
	fg := query.NewFakeGetter()
	fg.SetJSON(ifaceListPath, sampleList)
	fg.SetRaw("/show/interface/Wireguard0", []byte(`{"id":"Wireguard0","type":"Wireguard","state":"up"}`))
	fg.SetJSON("/show/ip/route", `[]`)
	fg.SetRaw("/show/running-config", []byte(`{"message":["!"]}`))
	q := query.NewQueries(query.Deps{Getter: fg, Logger: query.NopLogger(), IsOS5: func() bool { return true }})
	return q, fg
}

func TestDispatcher_IfCreatedInvalidatesInterfaceList(t *testing.T) {
	q, fg := primedQueries(t)
	d := NewDispatcher(q, NopLogger())
	d.Start()
	defer d.Stop()

	if _, err := q.Interfaces.List(context.Background()); err != nil {
		t.Fatalf("prime: %v", err)
	}
	primeCalls := fg.Calls(ifaceListPath)
	if primeCalls != 1 {
		t.Fatalf("prime: want 1 call, got %d", primeCalls)
	}

	d.Enqueue(Event{Type: EventIfCreated, ID: "Wireguard1"})

	waitFor(t, 100*time.Millisecond, func() bool {
		_, _ = q.Interfaces.List(context.Background())
		return fg.Calls(ifaceListPath) > primeCalls
	})

	if got := fg.Calls(ifaceListPath); got <= primeCalls {
		t.Errorf("after IfCreated: want a new fetch, got %d calls total", got)
	}
}

func TestDispatcher_IfDestroyedInvalidatesListAndItem(t *testing.T) {
	q, fg := primedQueries(t)
	d := NewDispatcher(q, NopLogger())
	d.Start()
	defer d.Stop()

	_, _ = q.Interfaces.List(context.Background())
	_, _ = q.Interfaces.Get(context.Background(), "Wireguard0")

	primeList := fg.Calls(ifaceListPath)
	primeItem := fg.Calls("/show/interface/Wireguard0")

	d.Enqueue(Event{Type: EventIfDestroyed, ID: "Wireguard0"})
	waitFor(t, 100*time.Millisecond, func() bool {
		_, _ = q.Interfaces.List(context.Background())
		_, _ = q.Interfaces.Get(context.Background(), "Wireguard0")
		return fg.Calls(ifaceListPath) > primeList && fg.Calls("/show/interface/Wireguard0") > primeItem
	})

	if fg.Calls(ifaceListPath) <= primeList {
		t.Errorf("list not re-fetched after IfDestroyed")
	}
	if fg.Calls("/show/interface/Wireguard0") <= primeItem {
		t.Errorf("item not re-fetched after IfDestroyed")
	}
}

func TestDispatcher_IfDestroyedInvalidatesWGServers(t *testing.T) {
	// When NDMS destroys an interface the VPN-server list must be
	// invalidated too — otherwise the system-tunnel UI keeps showing
	// a card that the router has already torn down.
	q, fg := primedQueries(t)
	d := NewDispatcher(q, NopLogger())
	d.Start()
	defer d.Stop()

	_, _ = q.WGServers.List(context.Background())
	primed := fg.Calls(ifaceListPath)

	d.Enqueue(Event{Type: EventIfDestroyed, ID: "Wireguard1"})
	waitFor(t, 100*time.Millisecond, func() bool {
		_, _ = q.WGServers.List(context.Background())
		return fg.Calls(ifaceListPath) > primed
	})

	if fg.Calls(ifaceListPath) <= primed {
		t.Errorf("WGServer list not re-fetched after IfDestroyed")
	}
}

func TestDispatcher_IfCreatedInvalidatesWGServers(t *testing.T) {
	q, fg := primedQueries(t)
	d := NewDispatcher(q, NopLogger())
	d.Start()
	defer d.Stop()

	_, _ = q.WGServers.List(context.Background())
	primed := fg.Calls(ifaceListPath)

	d.Enqueue(Event{Type: EventIfCreated, ID: "Wireguard5"})
	waitFor(t, 100*time.Millisecond, func() bool {
		_, _ = q.WGServers.List(context.Background())
		return fg.Calls(ifaceListPath) > primed
	})

	if fg.Calls(ifaceListPath) <= primed {
		t.Errorf("WGServer list not re-fetched after IfCreated")
	}
}

func TestDispatcher_IfLayerChangedConf_InvalidatesRunningConfig(t *testing.T) {
	q, fg := primedQueries(t)
	d := NewDispatcher(q, NopLogger())
	d.Start()
	defer d.Stop()

	_, _ = q.RunningConfig.Lines(context.Background())
	primed := fg.Calls("/show/running-config")

	d.Enqueue(Event{Type: EventIfLayerChanged, ID: "Wireguard0", Layer: "conf", Level: "running"})
	waitFor(t, 100*time.Millisecond, func() bool {
		_, _ = q.RunningConfig.Lines(context.Background())
		return fg.Calls("/show/running-config") > primed
	})

	if fg.Calls("/show/running-config") <= primed {
		t.Errorf("running-config not re-fetched after conf layer hook")
	}
}

func TestDispatcher_DedupsRepeatedEnqueues(t *testing.T) {
	q, _ := primedQueries(t)
	d := NewDispatcher(q, NopLogger())

	for i := 0; i < 100; i++ {
		d.Enqueue(Event{Type: EventIfCreated, ID: "Wireguard0"})
	}

	d.mu.Lock()
	n := len(d.pending)
	d.mu.Unlock()

	// EventIfCreated produces two invalidation keys (storeInterfaces +
	// storeWGServers); 100 identical enqueues still collapse to exactly
	// those two.
	if n != 2 {
		t.Errorf("pending size after 100 identical enqueues: want 2, got %d", n)
	}
}

func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestDispatcher_Stop_Idempotent(t *testing.T) {
	q, _ := primedQueries(t)
	d := NewDispatcher(q, NopLogger())
	d.Start()
	d.Stop()
	d.Stop() // must not panic
}

func TestDispatcher_Stop_WithoutStart_ReturnsImmediately(t *testing.T) {
	q, _ := primedQueries(t)
	d := NewDispatcher(q, NopLogger())
	// Stop without Start — must not block, must not panic.
	done := make(chan struct{})
	go func() { d.Stop(); close(done) }()
	select {
	case <-done:
		// Good
	case <-time.After(100 * time.Millisecond):
		t.Errorf("Stop without Start should return immediately")
	}
}
