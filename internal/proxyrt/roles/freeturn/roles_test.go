package freeturn

import (
	"context"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/awgmproto"
	"github.com/hoaxisr/awg-manager/internal/proxyrt"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/control"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/roles"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/roles/linkres"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/roles/netres"
)

type fakeLink struct{ err error }

func (f *fakeLink) State(context.Context) (awgmproto.State, error) {
	return awgmproto.State{}, f.err
}

func (f *fakeLink) Snapshot() (control.Snapshot, bool) { return control.Snapshot{}, false }

type nilRunner struct{}

func (nilRunner) Start(context.Context, []string) (int, error) { return 1, nil }
func (nilRunner) Stop(context.Context, int) error              { return nil }
func (nilRunner) AlivePID() (int, bool)                        { return 0, false }

type nilGate struct{}

func (nilGate) Check(context.Context, string, string, string, []string) error { return nil }

type nilSync struct{}

func (nilSync) List(context.Context, string) ([]linkres.LinkedTunnel, error) { return nil, nil }
func (nilSync) Sync(context.Context, string, string) (int, error)            { return 0, nil }

type nilOcc struct{}

func (nilOcc) OccupiedLocalListenPorts(context.Context) (map[int]bool, error) {
	return map[int]bool{}, nil
}

type memFW struct{ open map[string]bool }

func (m *memFW) Managed(context.Context) ([]netres.PortSpec, error) { return nil, nil }

func (m *memFW) Reconcile(_ context.Context, desired []netres.PortSpec) error {
	m.open = map[string]bool{}
	for _, s := range desired {
		m.open[s.Proto] = true
	}
	return nil
}

func ids(res []proxyrt.Resource) []proxyrt.ResourceID {
	var out []proxyrt.ResourceID
	for _, r := range res {
		out = append(out, r.ID())
	}
	return out
}

func TestClientChain(t *testing.T) {
	role, err := NewClient(ClientDeps{Instance: "default", Binary: "/opt/bin/ft-client",
		Link: &fakeLink{err: control.ErrNoSocket}, Runner: nilRunner{}, Gate: nilGate{},
		Sync: nilSync{}, Occ: nilOcc{}, Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	cfg := roles.FreeTurnClientConfig{Listen: "127.0.0.1:9001", Peer: "relay:3478"}
	got := ids(role.Resources(proxyrt.IntentEnabled, cfg, proxyrt.NewObservations()))
	want := []proxyrt.ResourceID{"listen_port", "process", "linked_endpoint"}
	if len(got) != len(want) {
		t.Fatalf("состав: %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("порядок: %v", got)
		}
	}
}

func TestClientDisabledLedgerIsProcessOnly(t *testing.T) {
	role, err := NewClient(ClientDeps{Instance: "default", Binary: "/opt/bin/ft-client",
		Link: &fakeLink{err: control.ErrNoSocket}, Runner: nilRunner{}, Gate: nilGate{},
		Sync: nilSync{}, Occ: nilOcc{}, Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	cfg := roles.FreeTurnClientConfig{Listen: "127.0.0.1:9001", Peer: "relay:3478"}
	got := ids(role.Resources(proxyrt.IntentDisabled, cfg, proxyrt.NewObservations()))
	if len(got) != 1 || got[0] != "process" {
		t.Fatalf("disabled клиент — только process (M11): %v", got)
	}
}

func TestServerChainAndPortProto(t *testing.T) {
	fw := &memFW{open: map[string]bool{}}
	role, err := NewServer(ServerDeps{Instance: "default", Binary: "/opt/bin/ft-server",
		Link: &fakeLink{err: control.ErrNoSocket}, Runner: nilRunner{}, Gate: nilGate{},
		FW: fw, Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	cfg := roles.FreeTurnServerConfig{Listen: "0.0.0.0:3478", Mode: "tcp", OpenFirewall: true}
	res := role.Resources(proxyrt.IntentEnabled, cfg, proxyrt.NewObservations())
	got := ids(res)
	want := []proxyrt.ResourceID{"process", "input_port"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("состав: %v", got)
	}
	// Протокол INPUT-правила следует за Mode (freeturn/server_firewall.go:18-21).
	input := res[1]
	obs, _ := input.Observe(context.Background())
	for _, s := range input.Plan(obs) {
		if err := input.Apply(context.Background(), s); err != nil {
			t.Fatal(err)
		}
	}
	if !fw.open["tcp"] {
		t.Fatalf("tcp-режим обязан открывать tcp-порт: %v", fw.open)
	}
}

func TestServerClosedFirewallDeclaresNoPorts(t *testing.T) {
	fw := &memFW{open: map[string]bool{}}
	role, _ := NewServer(ServerDeps{Instance: "default", Binary: "/opt/bin/ft-server",
		Link: &fakeLink{err: control.ErrNoSocket}, Runner: nilRunner{}, Gate: nilGate{},
		FW: fw, Now: time.Now})
	cfg := roles.FreeTurnServerConfig{Listen: "0.0.0.0:3478", Mode: "udp", OpenFirewall: false}
	res := role.Resources(proxyrt.IntentEnabled, cfg, proxyrt.NewObservations())
	input := res[1]
	obs, _ := input.Observe(context.Background())
	if steps := input.Plan(obs); len(steps) != 0 {
		t.Fatalf("выключенный OpenFirewall — портов нет: %v", steps)
	}
}

func TestClientResourcesAreLongLived(t *testing.T) {
	// I5: reconcile зовёт Resources дважды за проход и применяет по второму
	// списку. Пересозданный ресурс терял бы окно старта и backoff процесса —
	// анти-флаппинг сбрасывался бы на каждом прогоне.
	role, err := NewClient(ClientDeps{Instance: "default", Binary: "/opt/bin/ft-client",
		Link: &fakeLink{err: control.ErrNoSocket}, Runner: nilRunner{}, Gate: nilGate{},
		Sync: nilSync{}, Occ: nilOcc{}, Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	cfg := roles.FreeTurnClientConfig{Listen: "127.0.0.1:9001", Peer: "relay:3478"}
	first := role.Resources(proxyrt.IntentEnabled, cfg, proxyrt.NewObservations())
	second := role.Resources(proxyrt.IntentEnabled, cfg, proxyrt.NewObservations())
	if len(first) != len(second) {
		t.Fatalf("состав поплыл между вызовами: %v против %v", ids(first), ids(second))
	}
	for i := range first {
		if first[i] != second[i] {
			t.Fatalf("ресурс %s пересоздан между вызовами Resources", first[i].ID())
		}
	}
}

func TestServerResourcesAreLongLived(t *testing.T) {
	// Тот же I5 для сервера: пересоздание InputPort обнулило бы ведомость
	// разности (InputPort.prev) — прежний открытый порт перестал бы закрываться
	// при смене порта или режима.
	role, err := NewServer(ServerDeps{Instance: "default", Binary: "/opt/bin/ft-server",
		Link: &fakeLink{err: control.ErrNoSocket}, Runner: nilRunner{}, Gate: nilGate{},
		FW: &memFW{open: map[string]bool{}}, Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	cfg := roles.FreeTurnServerConfig{Listen: "0.0.0.0:3478", Mode: "udp", OpenFirewall: true}
	first := role.Resources(proxyrt.IntentEnabled, cfg, proxyrt.NewObservations())
	second := role.Resources(proxyrt.IntentEnabled, cfg, proxyrt.NewObservations())
	if len(first) != len(second) {
		t.Fatalf("состав поплыл между вызовами: %v против %v", ids(first), ids(second))
	}
	for i := range first {
		if first[i] != second[i] {
			t.Fatalf("ресурс %s пересоздан между вызовами Resources", first[i].ID())
		}
	}
}
