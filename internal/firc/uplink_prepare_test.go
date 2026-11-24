package firc

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/uplink"
)

func TestUplinkPreparationCreatesOnlyMissingLoopbackPool(t *testing.T) {
	a, m, _ := uplinkFixture(t)
	original := TunnelInbound{Listen: "br0", Port: 1099, Node: "selected-node"}
	m.tunnels[0].Inbounds = []TunnelInbound{original}
	target := uplink.Target{Engine: "firc", Ref: "managed"}
	if err := a.PrepareUplink(context.Background(), target); err != nil {
		t.Fatal(err)
	}
	got := m.tunnels[0]
	if len(got.Inbounds) != 2 || got.Inbounds[0] != original || !UplinkPoolInbound(got.Inbounds[1]) {
		t.Fatalf("existing inbound replaced or missing pool: %+v", got.Inbounds)
	}
	if m.tunnels[1].Enable || m.tunnels[1].Uplink.Ref != "untouched" {
		t.Fatal("neighbour modified")
	}
	count := m.putTunnelsCount
	if err := a.PrepareUplink(context.Background(), target); err != nil {
		t.Fatal(err)
	}
	if m.putTunnelsCount != count {
		t.Fatal("preparation is not idempotent")
	}
}

func TestUplinkPreparationRejectsInactiveDisabledAndUnknown(t *testing.T) {
	for _, mode := range []string{"inactive", "disabled", "missing"} {
		t.Run(mode, func(t *testing.T) {
			a, m, _ := uplinkFixture(t)
			target := uplink.Target{Engine: "firc", Ref: "managed"}
			switch mode {
			case "inactive":
				a.Active = func() bool { return false }
			case "disabled":
				m.tunnels[0].Enable = false
			case "missing":
				target.Ref = "missing"
			}
			if err := a.PrepareUplink(context.Background(), target); err == nil {
				t.Fatal("invalid target prepared")
			}
			if m.putTunnelsCount != 0 {
				t.Fatal("invalid preparation mutated config")
			}
		})
	}
}

func TestUplinkReleaseRemovesAutoCreatedLoopbackAndIsIdempotent(t *testing.T) {
	a, m, _ := uplinkFixture(t)
	original := TunnelInbound{Listen: "br0", Port: 1099, Node: "selected-node"}
	m.tunnels[0].Inbounds = []TunnelInbound{original}
	target := uplink.Target{Engine: "firc", Ref: "managed"}

	if err := a.PrepareUplink(context.Background(), target); err != nil {
		t.Fatal(err)
	}
	if len(m.tunnels[0].Inbounds) != 2 || m.tunnels[0].Inbounds[1].Listen != "lo" {
		t.Fatalf("expected auto lo inbound added, got %+v", m.tunnels[0].Inbounds)
	}
	recordPath := filepath.Join(a.Operator.ConfigDir(), uplinkDiagnosticInboundsFile)
	if _, err := os.Stat(recordPath); err != nil {
		t.Fatalf("expected diagnostic inbound record file at %s: %v", recordPath, err)
	}

	if err := a.ReleaseUplink(context.Background(), target); err != nil {
		t.Fatalf("ReleaseUplink: %v", err)
	}
	if len(m.tunnels[0].Inbounds) != 1 || m.tunnels[0].Inbounds[0] != original {
		t.Fatalf("expected only original br0 inbound after release, got %+v", m.tunnels[0].Inbounds)
	}
	if m.tunnels[1].Enable || m.tunnels[1].Uplink.Ref != "untouched" {
		t.Fatal("neighbour modified on release")
	}
	if _, err := os.Stat(recordPath); !os.IsNotExist(err) {
		t.Fatalf("expected record file cleaned up after last release, err=%v", err)
	}

	putsAfterRelease := m.putTunnelsCount
	if err := a.ReleaseUplink(context.Background(), target); err != nil {
		t.Fatalf("second ReleaseUplink: %v", err)
	}
	if m.putTunnelsCount != putsAfterRelease {
		t.Fatalf("ReleaseUplink is not idempotent: put count grew %d -> %d", putsAfterRelease, m.putTunnelsCount)
	}
}

func TestUplinkReleasePreservesPreexisting127LoopbackInbound(t *testing.T) {
	a, m, _ := uplinkFixture(t)
	preexisting := TunnelInbound{Listen: "127.0.0.1", Port: 1088, Node: TunnelNodePool}
	m.tunnels[0].Inbounds = []TunnelInbound{preexisting}
	target := uplink.Target{Engine: "firc", Ref: "managed"}

	if err := a.PrepareUplink(context.Background(), target); err != nil {
		t.Fatal(err)
	}
	if err := a.ReleaseUplink(context.Background(), target); err != nil {
		t.Fatalf("ReleaseUplink: %v", err)
	}
	if len(m.tunnels[0].Inbounds) != 1 || m.tunnels[0].Inbounds[0] != preexisting {
		t.Fatalf("preexisting 127.0.0.1 pool inbound was touched: %+v", m.tunnels[0].Inbounds)
	}
	if m.putTunnelsCount != 0 {
		t.Fatalf("unexpected PutTunnels calls: %d", m.putTunnelsCount)
	}
}

func TestUplinkReleaseOfflineAndMissingTunnel(t *testing.T) {
	a, m, _ := uplinkFixture(t)
	a.Active = func() bool { return false }
	userInbound := TunnelInbound{Listen: "br0", Port: 1099, Node: "selected-node"}
	diagInbound := TunnelInbound{Listen: "lo", Port: 49281, Node: TunnelNodePool}
	tuns := append([]Tunnel(nil), m.tunnels...)
	tuns[0].Inbounds = []TunnelInbound{userInbound, diagInbound}
	if err := a.Operator.SaveTunnelsFile(tuns); err != nil {
		t.Fatal(err)
	}

	if err := a.ReleaseUplink(context.Background(), uplink.Target{Engine: "firc", Ref: "managed"}); err != nil {
		t.Fatalf("offline ReleaseUplink: %v", err)
	}
	loaded, err := a.Operator.LoadTunnelsFile()
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded[0].Inbounds) != 1 || loaded[0].Inbounds[0] != userInbound {
		t.Fatalf("expected offline tunnels.yaml to drop lo:49281 and keep br0:1099, got %+v", loaded[0].Inbounds)
	}

	if err := a.ReleaseUplink(context.Background(), uplink.Target{Engine: "firc", Ref: "deleted-tunnel"}); err != nil {
		t.Fatalf("missing tunnel ReleaseUplink should succeed, got %v", err)
	}
}

// Сохранение туннелей из UI не переводит служебный вход без пароля в LAN (F898).
func TestEnsureInboundPort1099_KeepsLoopback(t *testing.T) {
	got := EnsureInboundPort1099([]Tunnel{{ID: "managed", Enable: true, Inbounds: []TunnelInbound{
		{Listen: "lo", Port: 49281, Node: TunnelNodePool},
		{Listen: "127.0.0.1", Port: 1099, Node: TunnelNodePool},
		{Listen: "", Port: 1098, Node: TunnelNodePool},
	}}}, 1099)[0].Inbounds
	if got[0].Listen != "lo" || got[1].Listen != "br0" || got[2].Listen != "br0" || len(got) != 3 {
		t.Fatalf("inbounds = %+v", got)
	}
}

// Вход, уже переведённый прежней версией на br0, снимается и при подготовке, и при
// освобождении; пользовательский вход на br0 с другим портом — нет.
func TestUplinkRemovesInboundMovedToLAN(t *testing.T) {
	user := TunnelInbound{Listen: "br0", Port: 1088, Node: TunnelNodePool}
	moved := TunnelInbound{Listen: "br0", Port: 49281, Node: TunnelNodePool}
	target := uplink.Target{Engine: "firc", Ref: "managed"}
	for _, tc := range []struct {
		name string
		run  func(*UplinkAdapter) error
		lo   int
	}{
		{"подготовка", func(a *UplinkAdapter) error { return a.PrepareUplink(context.Background(), target) }, 1},
		{"освобождение", func(a *UplinkAdapter) error { return a.ReleaseUplink(context.Background(), target) }, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, m, _ := uplinkFixture(t)
			m.tunnels[0].Inbounds = []TunnelInbound{user, moved}
			if err := a.Operator.saveUplinkInboundRecords(map[string]uint16{"managed": moved.Port}); err != nil {
				t.Fatal(err)
			}
			if err := tc.run(a); err != nil {
				t.Fatal(err)
			}
			got := m.tunnels[0].Inbounds
			if len(got) != 1+tc.lo || got[0] != user || (tc.lo == 1 && !UplinkPoolInbound(got[1])) {
				t.Fatalf("inbounds = %+v", got)
			}
		})
	}
}
