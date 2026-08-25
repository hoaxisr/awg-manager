package freeturn

import (
	"context"
	"errors"
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
func (nilSync) SetState(context.Context, string, bool) (int, error)          { return 0, nil }

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

// countSync и countFW — счётчики мутаций, доступных ролям FreeTurn: правка
// endpoint'ов связанных туннелей у клиента и INPUT-правила у сервера. NDMS
// ролям FreeTurn не выдан вовсе, и это тоже часть инварианта.
type countSync struct{ n int }

func (c *countSync) List(context.Context, string) ([]linkres.LinkedTunnel, error) {
	c.n++
	return nil, nil
}
func (c *countSync) Sync(context.Context, string, string) (int, error)   { c.n++; return 0, nil }
func (c *countSync) SetState(context.Context, string, bool) (int, error) { c.n++; return 0, nil }

// liveSync — связь с ОДНИМ поднятым туннелем. Пустой nilSync шов «роль →
// ресурс» не проверяет вовсе: при пустом списке любое желаемое состояние даёт
// нулевое расхождение, и константа вместо намерения проходит незамеченной.
type liveSync struct{ running bool }

// List СВЕРЯЕТ идентификатор клиента: роль обязана передать в ресурс имя
// своего инстанса, и без сверки чужое имя прошло бы незамеченным — список
// пуст, расхождение нулевое, шага нет, и всё молча.
func (l *liveSync) List(_ context.Context, clientID string) ([]linkres.LinkedTunnel, error) {
	if clientID != "default" {
		return nil, nil
	}
	return []linkres.LinkedTunnel{
		{ID: "t1", Endpoint: "127.0.0.1:9001", Running: l.running, Lifecycle: true},
	}, nil
}
func (l *liveSync) Sync(context.Context, string, string) (int, error)   { return 0, nil }
func (l *liveSync) SetState(context.Context, string, bool) (int, error) { return 0, nil }

type countFW struct{ n int }

func (f *countFW) Managed(context.Context) ([]netres.PortSpec, error) { f.n++; return nil, nil }
func (f *countFW) Reconcile(context.Context, []netres.PortSpec) error { f.n++; return nil }

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

func TestClientDisabledLedgerKeepsLinkedEndpoint(t *testing.T) {
	// M11 снимает с выключенного клиента доводку endpoint'а и приговор порта,
	// но не опускание связанных туннелей: без linked_endpoint в ведомости
	// выключение клиента оставляет туннель поднятым навсегда (амендмент B).
	role, err := NewClient(ClientDeps{Instance: "default", Binary: "/opt/bin/ft-client",
		Link: &fakeLink{err: control.ErrNoSocket}, Runner: nilRunner{}, Gate: nilGate{},
		Sync: nilSync{}, Occ: nilOcc{}, Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	cfg := roles.FreeTurnClientConfig{Listen: "127.0.0.1:9001", Peer: "relay:3478"}
	got := ids(role.Resources(proxyrt.IntentDisabled, cfg, proxyrt.NewObservations()))
	want := []proxyrt.ResourceID{"process", "linked_endpoint"}
	if len(got) != len(want) {
		t.Fatalf("disabled состав: %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("disabled порядок: %v", got)
		}
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

func TestClientResourcesDeclareWithoutTouchingRouter(t *testing.T) {
	// G1: Resources — чистая декларация. Приведение живёт в Apply ресурсов, и
	// ни одно намерение не даёт роли права мутировать что-либо самой.
	cfg := roles.FreeTurnClientConfig{Listen: "127.0.0.1:9001", Peer: "relay:3478"}
	for _, intent := range []proxyrt.Intent{
		proxyrt.IntentEnabled, proxyrt.IntentDisabled, proxyrt.IntentDeleted,
	} {
		sync := &countSync{}
		role, err := NewClient(ClientDeps{Instance: "default", Binary: "/opt/bin/ft-client",
			Link: &fakeLink{err: control.ErrNoSocket}, Runner: nilRunner{}, Gate: nilGate{},
			Sync: sync, Occ: nilOcc{}, Now: time.Now})
		if err != nil {
			t.Fatal(err)
		}
		role.Resources(intent, cfg, proxyrt.NewObservations())
		if sync.n != 0 {
			t.Fatalf("%s: за сборку декларации ушло %d правок связанных туннелей", intent, sync.n)
		}
	}
}

func TestServerResourcesDeclareWithoutTouchingRouter(t *testing.T) {
	// То же для серверной роли: INPUT-порты открывает Apply ресурса
	// input_port, а не ветка декларации.
	cfg := roles.FreeTurnServerConfig{Listen: "0.0.0.0:3478", Mode: "udp", OpenFirewall: true}
	for _, intent := range []proxyrt.Intent{
		proxyrt.IntentEnabled, proxyrt.IntentDisabled, proxyrt.IntentDeleted,
	} {
		fw := &countFW{}
		role, err := NewServer(ServerDeps{Instance: "default", Binary: "/opt/bin/ft-server",
			Link: &fakeLink{err: control.ErrNoSocket}, Runner: nilRunner{}, Gate: nilGate{},
			FW: fw, Now: time.Now})
		if err != nil {
			t.Fatal(err)
		}
		role.Resources(intent, cfg, proxyrt.NewObservations())
		if fw.n != 0 {
			t.Fatalf("%s: за сборку декларации ушло %d обращений к firewall", intent, fw.n)
		}
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

// Локальный bind не открывает порт наружу: старый путь через
// listenfirewall.WANListenPort при 127.0.0.1 правила не ставил.
func TestServerLocalBindDeclaresNoPorts(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:3478", "192.168.0.1:3478"} {
		cfg := roles.FreeTurnServerConfig{Listen: addr, Connect: "127.0.0.1:51820", Mode: "udp", OpenFirewall: true}
		if got := serverPorts(cfg, true); got != nil {
			t.Fatalf("serverPorts(%q) = %v, ожидали nil (локальный bind наружу не открываем)", addr, got)
		}
	}
	wan := roles.FreeTurnServerConfig{Listen: "0.0.0.0:3478", Connect: "127.0.0.1:51820", Mode: "udp", OpenFirewall: true}
	if got := serverPorts(wan, true); len(got) != 1 || got[0].Port != 3478 {
		t.Fatalf("serverPorts(0.0.0.0:3478) = %v, ожидали один порт 3478", got)
	}
	over := roles.FreeTurnServerConfig{Listen: "0.0.0.0:70000", Connect: "127.0.0.1:51820", Mode: "udp", OpenFirewall: true}
	if got := serverPorts(over, true); got != nil {
		t.Fatalf("serverPorts(:70000) = %v, ожидали nil (порт вне диапазона)", got)
	}
}

// Шов «роль → ресурс»: намерение обязано доехать до ЖЕЛАЕМОГО состояния
// связанных туннелей. Состав ведомости этого не ловит — с константой вместо
// намерения он тот же самый, а выключенный клиент туннель не опустит никогда.
func TestClientIntentReachesLinkedResource(t *testing.T) {
	cfg := roles.FreeTurnClientConfig{Listen: "127.0.0.1:9001", Peer: "relay:3478"}
	for _, c := range []struct {
		intent  proxyrt.Intent
		running bool
		op      string
	}{
		{proxyrt.IntentEnabled, false, "start"},
		{proxyrt.IntentDisabled, true, "stop"},
	} {
		role, err := NewClient(ClientDeps{Instance: "default", Binary: "/opt/bin/ft-client",
			Link: &fakeLink{err: control.ErrNoSocket}, Runner: nilRunner{}, Gate: nilGate{},
			Sync: &liveSync{running: c.running}, Occ: nilOcc{}, Now: time.Now})
		if err != nil {
			t.Fatal(err)
		}
		var le proxyrt.Resource
		res := role.Resources(c.intent, cfg, proxyrt.NewObservations())
		for _, r := range res {
			if r.ID() == "linked_endpoint" {
				le = r
			}
		}
		if le == nil {
			t.Fatalf("%s: linked_endpoint не объявлен: %v", c.intent, ids(res))
		}
		obs, err := le.Observe(context.Background())
		if err != nil {
			t.Fatalf("%s: observe: %v", c.intent, err)
		}
		steps := le.Plan(obs)
		if len(steps) != 1 || steps[0].Op != c.op {
			t.Fatalf("%s: план %+v, ожидали %q", c.intent, steps, c.op)
		}
	}
}

// failGate — отказ пробы бинаря: единственный способ взвести паузу
// повторного старта через публичную поверхность роли.
type failGate struct{}

func (failGate) Check(context.Context, string, string, string, []string) error {
	return errors.New("пин бинаря не обновлён")
}

// procOf достаёт ресурс процесса из ведомости.
func procOf(t *testing.T, res []proxyrt.Resource) proxyrt.Resource {
	t.Helper()
	for _, r := range res {
		if r.ID() == roles.RProcess {
			return r
		}
	}
	t.Fatalf("process не объявлен: %v", ids(res))
	return nil
}

// Шов «роль → ресурс процесса» у обеих ролей FreeTurn: правку конфига руками
// (PATCH клиента и сервера — оба сбрасывали паузу в старом мире) менеджер
// проводит через ResetStartBackoff роли, и без этого шва она уйдёт в пустоту.
func TestResetStartBackoffReachesProcess(t *testing.T) {
	client, err := NewClient(ClientDeps{Instance: "default", Binary: "/opt/bin/ft-client",
		Link: &fakeLink{err: control.ErrNoSocket}, Runner: nilRunner{}, Gate: failGate{},
		Sync: nilSync{}, Occ: nilOcc{}, Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(ServerDeps{Instance: "default", Binary: "/opt/bin/ft-server",
		Link: &fakeLink{err: control.ErrNoSocket}, Runner: nilRunner{}, Gate: failGate{},
		FW: &countFW{}, Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name  string
		reset func()
		res   []proxyrt.Resource
	}{
		{"клиент", client.ResetStartBackoff,
			client.Resources(proxyrt.IntentEnabled,
				roles.FreeTurnClientConfig{Listen: "127.0.0.1:9001", Peer: "relay:3478"},
				proxyrt.NewObservations())},
		{"сервер", server.ResetStartBackoff,
			server.Resources(proxyrt.IntentEnabled,
				roles.FreeTurnServerConfig{Listen: "0.0.0.0:3478", Mode: "udp"},
				proxyrt.NewObservations())},
	} {
		proc := procOf(t, c.res)
		obs, err := proc.Observe(context.Background())
		if err != nil {
			t.Fatalf("%s: observe: %v", c.name, err)
		}
		steps := proc.Plan(obs)
		if len(steps) != 1 || steps[0].Op != "start" {
			t.Fatalf("%s: план %+v, ожидали start", c.name, steps)
		}
		if err := proc.Apply(context.Background(), steps[0]); err == nil {
			t.Fatalf("%s: старт обязан упасть на гейте", c.name)
		}
		if proc.RecheckAfter() <= 0 {
			t.Fatalf("%s: пауза повторного старта не взведена", c.name)
		}
		c.reset()
		if got := proc.RecheckAfter(); got != 0 {
			t.Fatalf("%s: сброс роли не дошёл до процесса, пауза %v", c.name, got)
		}
	}
}
