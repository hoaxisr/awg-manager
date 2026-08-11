package nwg

import (
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/tunnel"
)

func TestClassifyNWGState(t *testing.T) {
	slotPresent := func(int) bool { return true }
	slotAbsent := func(int) bool { return false }

	cases := []struct {
		name        string
		rci         NWGState
		supportsASC bool
		hasSlot     func(int) bool
		want        tunnel.State
	}{
		{"running+online -> Running",
			NWGState{ConfLayer: "running", PeerOnline: true}, false, slotAbsent, tunnel.StateRunning},
		{"proxy running+offline, no slot -> Broken",
			NWGState{ConfLayer: "running", PeerOnline: false, PeerRemoteAddr: "127.0.0.1", PeerRemotePort: 51958}, false, slotAbsent, tunnel.StateBroken},
		{"proxy running+offline, remote not localhost -> Broken",
			NWGState{ConfLayer: "running", PeerOnline: false, PeerRemoteAddr: "46.149.74.35", PeerRemotePort: 443}, false, slotPresent, tunnel.StateBroken},
		{"proxy running+offline, coherent -> Starting",
			NWGState{ConfLayer: "running", PeerOnline: false, PeerRemoteAddr: "127.0.0.1", PeerRemotePort: 51958}, false, slotPresent, tunnel.StateStarting},
		{"ASC running+offline -> Starting (no kmod)",
			NWGState{ConfLayer: "running", PeerOnline: false, PeerRemoteAddr: "1.2.3.4", PeerRemotePort: 51820}, true, slotAbsent, tunnel.StateStarting},
		{"disabled -> Stopped",
			NWGState{ConfLayer: "disabled"}, false, slotAbsent, tunnel.StateStopped},
		{"unknown conf -> Unknown",
			NWGState{ConfLayer: ""}, false, slotAbsent, tunnel.StateUnknown},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := classifyNWGState(c.rci, c.supportsASC, c.hasSlot, time.Now())
			if got != c.want {
				t.Errorf("classifyNWGState = %v, want %v", got, c.want)
			}
		})
	}
}

func TestClassifyNWGState_RunningSkipsSlotCheck(t *testing.T) {
	called := false
	probe := func(int) bool { called = true; return false }
	got := classifyNWGState(NWGState{ConfLayer: "running", PeerOnline: true}, false, probe, time.Now())
	if got != tunnel.StateRunning {
		t.Fatalf("got %v, want Running", got)
	}
	if called {
		t.Error("hasProxySlot must NOT be called when peer is online")
	}
}

func TestClassifyNWGStateASCBrokenAfterTimeout(t *testing.T) {
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	base := NWGState{ConfLayer: "running", PeerOnline: false, LastHandshake: neverHandshake}

	// Хендшейка не было ни разу и якоря времени нет вовсе — ровно то, что
	// отдаёт роутер для недостижимого endpoint: link=down, connected="no",
	// uptime отсутствует. Сломан; «подъём идёт прямо сейчас» знает только
	// окно оркестратора, а не классификатор (#702).
	nohs := base
	nohs.Connected = ""
	if got := classifyNWGState(nohs, true, nil, now); got != tunnel.StateBroken {
		t.Fatalf("хендшейка не было и нет времени подъёма: ожидался Broken, получен %v", got)
	}

	// Свежий интерфейс без единого хендшейка — тоже сломан по той же причине:
	// возраст интерфейса ничего не говорит о том, что подъём идёт сейчас.
	fresh := base
	fresh.Connected = now.Add(-30 * time.Second).Format(time.RFC3339)
	if got := classifyNWGState(fresh, true, nil, now); got != tunnel.StateBroken {
		t.Fatalf("свежий интерфейс без хендшейка: ожидался Broken, получен %v", got)
	}

	// Поднят давно, хендшейка не было ни разу — сломан.
	stale := base
	stale.Connected = now.Add(-10 * time.Minute).Format(time.RFC3339)
	if got := classifyNWGState(stale, true, nil, now); got != tunnel.StateBroken {
		t.Fatalf("залипший интерфейс: ожидался Broken, получен %v", got)
	}

	// Хендшейк был, но давно — тоже сломан.
	old := base
	old.Connected = now.Add(-10 * time.Minute).Format(time.RFC3339)
	old.LastHandshake = int64((7 * time.Minute).Seconds())
	if got := classifyNWGState(old, true, nil, now); got != tunnel.StateBroken {
		t.Fatalf("протухший хендшейк: ожидался Broken, получен %v", got)
	}

	// Хендшейк свежий, но неизвестен момент подъёма — не гадаем, Starting.
	unknown := base
	unknown.Connected = ""
	unknown.LastHandshake = 30
	if got := classifyNWGState(unknown, true, nil, now); got != tunnel.StateStarting {
		t.Fatalf("без времени подъёма: ожидался Starting, получен %v", got)
	}
}
