package events

import (
	"context"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms/query"
)

const ifaceListPath = "/show/interface/"

const sampleList = `{"Wireguard0": {"id":"Wireguard0","interface-name":"nwg0","type":"Wireguard","state":"up"}}`

func primedQueries(_ *testing.T) (*query.Queries, *query.FakeGetter) {
	fg := query.NewFakeGetter()
	fg.SetJSON(ifaceListPath, sampleList)
	fg.SetRaw("/show/interface/Wireguard0", []byte(`{"id":"Wireguard0","interface-name":"nwg0","type":"Wireguard","state":"up"}`))
	fg.SetRaw("/show/interface/Wireguard1", []byte(`{"id":"Wireguard1","interface-name":"nwg1","type":"Wireguard","state":"up"}`))
	fg.SetJSON("/show/ip/route", `[]`)
	fg.SetRaw("/show/running-config", []byte(`{"message":["!"]}`))
	q := query.NewQueries(query.Deps{Getter: fg, Logger: query.NopLogger(), IsOS5: func() bool { return true }})
	return q, fg
}

// === Event-sourced InterfaceStore behaviour ===

// IfCreated must apply via OnCreated which fetches ONLY the new id —
// it must NOT re-fetch the full list.
func TestDispatcher_IfCreated_FetchesOnlyNewID(t *testing.T) {
	q, fg := primedQueries(t)
	d := NewDispatcher(q, NopLogger())
	d.Start()
	defer d.Stop()

	if _, err := q.Interfaces.List(context.Background()); err != nil {
		t.Fatalf("prime: %v", err)
	}
	primeList := fg.Calls(ifaceListPath)

	d.Enqueue(Event{Type: EventIfCreated, ID: "Wireguard1"})

	waitFor(t, 200*time.Millisecond, func() bool {
		return fg.Calls("/show/interface/Wireguard1") > 0
	})

	if got := fg.Calls("/show/interface/Wireguard1"); got != 1 {
		t.Errorf("after IfCreated: want 1 fetch of new id, got %d", got)
	}
	// Critical: list endpoint must NOT have been re-fetched.
	if got := fg.Calls(ifaceListPath); got != primeList {
		t.Errorf("list must NOT be re-fetched after IfCreated, before=%d after=%d", primeList, got)
	}
	// And the new entry must now be visible from Get without further HTTP.
	if got, _ := q.Interfaces.Get(context.Background(), "Wireguard1"); got == nil {
		t.Errorf("Wireguard1 must be queryable after OnCreated")
	}
}

// IfDestroyed must be a pure in-memory delete — no HTTP, no list refetch.
func TestDispatcher_IfDestroyed_NoHTTP(t *testing.T) {
	q, fg := primedQueries(t)
	d := NewDispatcher(q, NopLogger())
	d.Start()
	defer d.Stop()

	_, _ = q.Interfaces.List(context.Background())
	primeList := fg.Calls(ifaceListPath)
	primeItem := fg.Calls("/show/interface/Wireguard0")

	d.Enqueue(Event{Type: EventIfDestroyed, ID: "Wireguard0"})

	// Wait for the entry to disappear from cache (via OnDestroyed).
	waitFor(t, 200*time.Millisecond, func() bool {
		got, _ := q.Interfaces.Get(context.Background(), "Wireguard0")
		return got == nil
	})

	if got, _ := q.Interfaces.Get(context.Background(), "Wireguard0"); got != nil {
		t.Errorf("Wireguard0 must be removed from cache, got %#v", got)
	}
	// No HTTP for the destroy path on InterfaceStore.
	if got := fg.Calls(ifaceListPath); got != primeList {
		t.Errorf("list must NOT be re-fetched on IfDestroyed, before=%d after=%d", primeList, got)
	}
	if got := fg.Calls("/show/interface/Wireguard0"); got != primeItem {
		t.Errorf("item must NOT be re-fetched on IfDestroyed, before=%d after=%d", primeItem, got)
	}
}

// IfLayerChanged must patch in place — no HTTP for the InterfaceStore.
func TestDispatcher_IfLayerChanged_NoHTTPOnInterfaces(t *testing.T) {
	q, fg := primedQueries(t)
	d := NewDispatcher(q, NopLogger())
	d.Start()
	defer d.Stop()

	_, _ = q.Interfaces.List(context.Background())
	primeList := fg.Calls(ifaceListPath)
	primeItem := fg.Calls("/show/interface/Wireguard0")

	d.Enqueue(Event{Type: EventIfLayerChanged, ID: "Wireguard0", Layer: "conf", Level: "disabled"})

	waitFor(t, 200*time.Millisecond, func() bool {
		d, _ := q.Interfaces.GetDetails(context.Background(), "Wireguard0")
		return d != nil && d.ConfLayer == "disabled"
	})

	if got := fg.Calls(ifaceListPath); got != primeList {
		t.Errorf("list re-fetched on IfLayerChanged, before=%d after=%d", primeList, got)
	}
	if got := fg.Calls("/show/interface/Wireguard0"); got != primeItem {
		t.Errorf("item re-fetched on IfLayerChanged, before=%d after=%d", primeItem, got)
	}
}

// === Legacy InvalidateAll path for non-Interface stores ===

func TestDispatcher_IfDestroyed_InvalidatesWGServers(t *testing.T) {
	q, fg := primedQueries(t)
	d := NewDispatcher(q, NopLogger())
	d.Start()
	defer d.Stop()

	_, _ = q.WGServers.List(context.Background())
	primed := fg.Calls(ifaceListPath)

	d.Enqueue(Event{Type: EventIfDestroyed, ID: "Wireguard1"})
	waitFor(t, 200*time.Millisecond, func() bool {
		_, _ = q.WGServers.List(context.Background())
		return fg.Calls(ifaceListPath) > primed
	})

	if fg.Calls(ifaceListPath) <= primed {
		t.Errorf("WGServer list not re-fetched after IfDestroyed")
	}
}

func TestDispatcher_IfCreated_InvalidatesWGServers(t *testing.T) {
	q, fg := primedQueries(t)
	d := NewDispatcher(q, NopLogger())
	d.Start()
	defer d.Stop()

	_, _ = q.WGServers.List(context.Background())
	primed := fg.Calls(ifaceListPath)

	d.Enqueue(Event{Type: EventIfCreated, ID: "Wireguard5"})
	waitFor(t, 200*time.Millisecond, func() bool {
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
	waitFor(t, 200*time.Millisecond, func() bool {
		_, _ = q.RunningConfig.Lines(context.Background())
		return fg.Calls("/show/running-config") > primed
	})

	if fg.Calls("/show/running-config") <= primed {
		t.Errorf("running-config not re-fetched after conf layer hook")
	}
}

// === Worker lifecycle ===

func TestDispatcher_Stop_Idempotent(t *testing.T) {
	q, _ := primedQueries(t)
	d := NewDispatcher(q, NopLogger())
	d.Start()
	d.Stop()
	d.Stop()
}

func TestDispatcher_Stop_WithoutStart_ReturnsImmediately(t *testing.T) {
	q, _ := primedQueries(t)
	d := NewDispatcher(q, NopLogger())
	done := make(chan struct{})
	go func() { d.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		t.Errorf("Stop without Start should return immediately")
	}
}

// === Helpers ===

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
