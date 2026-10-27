package pingcheck

import (
	"context"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/httpprobe"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel/wg"
)

// peerWG: пир есть только у перечисленных устройств.
type peerWG struct{ withPeer map[string]bool }

func (c peerWG) Show(_ context.Context, iface string) (*wg.ShowResult, error) {
	return &wg.ShowResult{HasPeer: c.withPeer[iface]}, nil
}

// F626: opkgtunN, созданный NDMS без нашего конфига (старт на буте упал),
// не «работающий туннель» — монитор на нём лечил бы link toggle-ем вечно.
func TestStartMonitoringAllRunning_SkipsDeviceWithoutPeer(t *testing.T) {
	orig, origExists := httpprobe.Client, deviceExists
	t.Cleanup(func() { httpprobe.Client, deviceExists = orig, origExists })
	httpprobe.Client = okDoer{}
	deviceExists = func(string) bool { return true }
	stubRunCmd(t)

	store := newTunnelStore(t)
	for _, rec := range []struct{ id, backend, raw string }{
		{"awg15", "kernel", ""},
		{"awg16", "kernel", ""},
		// wdtt-raw — tun прокси-рантайма, не wg: пира у него не бывает,
		// проверка ему не применима.
		{"wdttraw-de", "wdtt-raw", "opkgtun18"},
	} {
		if err := store.Create(&storage.AWGTunnel{ID: rec.id, Name: rec.id, Backend: rec.backend, RawKernelIface: rec.raw,
			Peer:      storage.AWGPeer{PublicKey: "pk", Endpoint: "198.51.100.7:51820"},
			PingCheck: &storage.TunnelPingCheck{Enabled: true, Method: "http", Interval: 60, FailThreshold: 3},
		}); err != nil {
			t.Fatal(err)
		}
	}
	lb := NewLogBuffer()
	t.Cleanup(lb.Stop)
	ctx, cancel := context.WithCancel(context.Background())
	s := &Service{tunnels: store, wg: peerWG{withPeer: map[string]bool{"opkgtun16": true}},
		logBuffer: lb, ctx: ctx, monitors: map[string]*tunnelMonitor{}}
	t.Cleanup(cancel)
	t.Cleanup(s.StopMonitoringAll)

	s.StartMonitoringAllRunning()

	s.mu.RLock()
	_, gotNoPeer := s.monitors["awg15"]
	_, gotPeer := s.monitors["awg16"]
	_, gotRaw := s.monitors["wdttraw-de"]
	s.mu.RUnlock()
	if gotNoPeer {
		t.Error("монитор заведён на устройстве без пира (старт не состоялся)")
	}
	if !gotPeer {
		t.Error("работающий туннель с пиром остался без монитора")
	}
	if !gotRaw {
		t.Error("wdtt-raw остался без монитора: проверка пира к нему не применима")
	}
}
