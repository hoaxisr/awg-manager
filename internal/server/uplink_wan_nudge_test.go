package server

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/events"
	"github.com/hoaxisr/awg-manager/internal/uplink"
)

type nudgeTestProviders struct{}

func (nudgeTestProviders) Providers() []uplink.Provider {
	return []uplink.Provider{{ID: "ISP", Device: "eth3"}}
}

// Хук интерфейса приходит и на каждый наш туннель; подсказка uplink — только
// на WAN-провайдера и одна на хук.
func TestUplinkWANChangeNudge_PublishesOnlyForProvider(t *testing.T) {
	store, err := uplink.OpenStore(filepath.Join(t.TempDir(), "policies.json"))
	if err != nil {
		t.Fatal(err)
	}
	bus := events.NewBus()
	_, ch, unsub := bus.Subscribe()
	defer unsub()
	nudge := uplinkWANChangeNudge(bus, uplink.New(store, nudgeTestProviders{}, nil))

	nudge("OpkgTun0", "opkgtun0")
	nudge("", "awgm0")
	select {
	case ev := <-ch:
		t.Fatalf("tunnel hook published: %+v", ev)
	case <-time.After(50 * time.Millisecond):
	}

	nudge("", "eth3")
	select {
	case ev := <-ch:
		data, _ := ev.Data.(events.ResourceInvalidatedEvent)
		if data.Resource != events.ResourceUplink || data.Reason != "wan-changed" {
			t.Fatalf("event = %+v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("no uplink event for WAN provider hook")
	}
	select {
	case ev := <-ch:
		t.Fatalf("second event for one hook: %+v", ev)
	case <-time.After(50 * time.Millisecond):
	}
}
