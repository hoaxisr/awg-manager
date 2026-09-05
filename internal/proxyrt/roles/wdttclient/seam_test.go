package wdttclient

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/awgmproto"
	"github.com/hoaxisr/awg-manager/internal/proxyrt"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/control"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/roles"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/roles/linkres"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/roles/roletest"
)

// ── фикстура шва ────────────────────────────────────────────────

// seamLink — связь с живым клиентом: адрес и MTU уже пришли от сервера
// (RAWCONF), tun прикреплён.
//
// Значения снимка НАРОЧНО не совпадают ни с одной прод-константой
// (10.77.3.9 — не из пула 10.70.x, MTU 1342 — не 1280 и не 1300): иначе
// мутант, вернувший константу, случайно совпал бы с ожиданием, и пин
// молчал бы.
type seamLink struct {
	now  func() time.Time
	addr string
	mtu  int
}

func (l *seamLink) state() awgmproto.State {
	return awgmproto.State{
		Role: "client", Instance: "default", PID: 4321,
		Address: l.addr, MTU: l.mtu,
		Tun: &awgmproto.TunState{Iface: "opkgtun18", Attached: true},
	}
}
func (l *seamLink) State(context.Context) (awgmproto.State, error) { return l.state(), nil }
func (l *seamLink) Snapshot() (control.Snapshot, bool) {
	return control.Snapshot{State: l.state(), At: l.now()}, true
}
func (l *seamLink) AttachTun(context.Context, string, *os.File) error {
	panic("attach-tun в тесте шва: снимок обязан говорить, что tun уже прикреплён")
}
func (l *seamLink) DetachTun(context.Context) error { return nil }

// busyOcc — все порты пула заняты, кроме одного. Так проверяется, ОТКУДА
// роль берёт желаемый listen: с чужим портом цепочка не сойдётся.
type busyOcc struct{ free int }

func (o busyOcc) OccupiedLocalListenPorts(context.Context) (map[int]bool, error) {
	busy := map[int]bool{}
	for p := 9000; p <= 9200; p++ {
		if p != o.free {
			busy[p] = true
		}
	}
	return busy, nil
}

type clientSeam struct {
	role *Role
	ndms *roletest.NDMS
}

func newClientSeam(t *testing.T, link *seamLink, occ linkres.Occupancy) clientSeam {
	t.Helper()
	now := func() time.Time { return time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC) }
	link.now = now
	p := clientSeam{ndms: roletest.NewNDMS()}
	if occ == nil {
		occ = nilOcc{}
	}
	r, err := New(Deps{
		Instance: "default", Binary: "/opt/bin/wt-client",
		Link: link, Runner: nilRunner{}, Gate: nilGate{},
		Cmds: p.ndms, Query: p.ndms,
		Policies: nilPolicies{}, Permit: nilPermit{},
		Hooks: nilHooks{}, Registry: &memRegistry{m: map[string]linkres.ExitInfo{}},
		Sync: nilSync{}, Occ: occ, Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	p.role = r
	return p
}

func seamClientCfg() roles.WdttClientConfig {
	c := rawCfg()
	c.Name = "Германия"
	// Членство в политике — предмет других пинов, и фикстура политик здесь
	// пустая: без этого цепочка упирается в policy_membership и до целевых
	// шагов не доходит.
	c.Policies = nil
	return c
}

// ── RT8 ─────────────────────────────────────────────────────────

// Адрес, маска и MTU интерфейса приходят ИЗ СНИМКА процесса: их назначает
// VPS (RAWCONF), а не наш конфиг. Подменённые константой, они уводят трафик
// мимо туннеля — и мутация «вернуть константный AddrWant» проходила по всему
// дереву незамеченной.
func TestSeam_ClientAddressComesFromProcessSnapshot(t *testing.T) {
	p := newClientSeam(t, &seamLink{addr: "10.77.3.9", mtu: 1342}, nil)
	roletest.Converge(t, p.role, seamClientCfg(), proxyrt.IntentEnabled)

	facts, ok := p.ndms.Snapshot("OpkgTun18")
	if !ok {
		t.Fatal("интерфейс клиента не заведён в NDMS")
	}
	if facts.Address != "10.77.3.9" {
		t.Errorf("адрес %q, ждали из снимка 10.77.3.9", facts.Address)
	}
	if facts.Mask != "255.255.255.255" {
		t.Errorf("маска %q, ждали 255.255.255.255", facts.Mask)
	}
	if facts.MTU != 1342 {
		t.Errorf("MTU %d, ждали из снимка 1342", facts.MTU)
	}
}

// Негодный MTU от сервера заменяется фолбэком, а не уезжает на интерфейс:
// значение ниже 576 сделало бы связь неработоспособной.
//
// Фикстура — 100, а НЕ 0, и это принципиально: нуль нейтрализует нижестоящий
// гард `want.MTU > 0`, шаг SetMTU не планируется вовсе, а видимые 1300
// приносит create-дефолт — ДРУГОЙ механизм. С нулём тест оставался зелёным
// при выпиленном фолбэке (нашло ревью). Со 100 мутант даёт SetMTU(100).
func TestSeam_ClientMTUFallsBackOnGarbage(t *testing.T) {
	p := newClientSeam(t, &seamLink{addr: "10.77.3.9", mtu: 100}, nil)
	roletest.Converge(t, p.role, seamClientCfg(), proxyrt.IntentEnabled)

	facts, _ := p.ndms.Snapshot("OpkgTun18")
	if facts.MTU != 1300 {
		t.Errorf("MTU %d, ждали фолбэк 1300", facts.MTU)
	}
}

// ── RT13 ────────────────────────────────────────────────────────

// Метка владения и уровень безопасности доезжают до NDMS.
//
// Ожидание — ЛИТЕРАЛ, и это здесь принципиально: sweep-фикстуры строят своё
// ожидание тем же `roles.ClientDescription`, что и прод, то есть сверяют код
// сам с собой. Подмена метки на чужую проходила незамеченной — а значит
// уборщик перестал бы признавать интерфейс своим, и тот жил бы сиротой вечно.
func TestSeam_ClientOwnershipLabelReachesNDMS(t *testing.T) {
	p := newClientSeam(t, &seamLink{addr: "10.77.3.9", mtu: 1342}, nil)
	roletest.Converge(t, p.role, seamClientCfg(), proxyrt.IntentEnabled)

	facts, _ := p.ndms.Snapshot("OpkgTun18")
	if want := "AWGM WDTT Raw Client: Германия"; facts.Description != want {
		t.Errorf("метка владения %q, ждали %q", facts.Description, want)
	}
	if facts.SecurityLevel != "public" {
		t.Errorf("уровень безопасности %q, ждали public", facts.SecurityLevel)
	}
}

// ── RT36, клиентская половина ───────────────────────────────────

// Клиент — аплинк: он обязан стать КАНДИДАТОМ в default route политики.
// Снятая кандидатура означает, что селективная маршрутизация теряет выход.
func TestSeam_ClientIsDefaultRouteCandidate(t *testing.T) {
	p := newClientSeam(t, &seamLink{addr: "10.77.3.9", mtu: 1342}, nil)
	roletest.Converge(t, p.role, seamClientCfg(), proxyrt.IntentEnabled)

	got := p.ndms.ExitOf("OpkgTun18")
	if !got.DefaultRoute {
		t.Error("клиент не объявлен кандидатом в default route — политика останется без выхода")
	}
	if !got.IPGlobal {
		t.Error("ip global не взведён")
	}
}

// ── RT37 ────────────────────────────────────────────────────────

// Желаемый listen берётся ИЗ КОНФИГА. Доказательство от противного: пул
// портов занят целиком, кроме указанного в конфиге, — цепочка сходится
// только если роль попросила именно его.
func TestSeam_ClientListenComesFromConfig(t *testing.T) {
	cfg := seamClientCfg()
	cfg.Listen = "127.0.0.1:9123"
	p := newClientSeam(t, &seamLink{addr: "10.77.3.9", mtu: 1342}, busyOcc{free: 9123})
	roletest.Converge(t, p.role, cfg, proxyrt.IntentEnabled)
}
