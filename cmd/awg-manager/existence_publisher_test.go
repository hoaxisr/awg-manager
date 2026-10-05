package main

import (
	"testing"

	"github.com/hoaxisr/awg-manager/internal/events"
)

// publish=false (своё создание) — ни одной публикации; publish=true —
// инвалидация tunnels и servers.
func TestExistencePublisher(t *testing.T) {
	bus := events.NewBus()
	_, ch, unsub := bus.Subscribe()
	defer unsub()
	pub := existencePublisher(bus)

	pub(false)
	select {
	case ev := <-ch:
		t.Fatalf("publish=false опубликовал %#v", ev)
	default:
	}

	pub(true)
	got := map[events.Resource]bool{}
	for len(ch) > 0 {
		ev := <-ch
		if inv, ok := ev.Data.(events.ResourceInvalidatedEvent); ok {
			got[inv.Resource] = true
		}
	}
	if !got[events.ResourceTunnels] || !got[events.ResourceServers] || len(got) != 2 {
		t.Fatalf("publish=true: %v, want tunnels и servers", got)
	}
}
