package nwg

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/ndms/command"
	"github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/ndms/transport"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/sys/exec"
)

// payloadPoster — RCI-постер, хранящий payload'ы сериализованными: ассерты
// сверяют литерал JSON (ключи map упорядочены json.Marshal).
type payloadPoster struct {
	mu       sync.Mutex
	payloads []string
}

func (p *payloadPoster) Post(_ context.Context, payload any) (json.RawMessage, error) {
	b, _ := json.Marshal(payload)
	p.mu.Lock()
	p.payloads = append(p.payloads, string(b))
	p.mu.Unlock()
	return json.RawMessage(`{}`), nil
}

func (p *payloadPoster) has(substr string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, s := range p.payloads {
		if strings.Contains(s, substr) {
			return true
		}
	}
	return false
}

func (p *payloadPoster) list() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.payloads...)
}

func newLifecycleOperator(t *testing.T, asc, asc3 bool) (*OperatorNativeWG, *procStub, *payloadPoster) {
	t.Helper()
	km, stub := newKmodManagerForTest()
	poster := &payloadPoster{}
	q := query.NewQueries(query.Deps{Getter: query.NewFakeGetter(), Logger: query.NopLogger(), IsOS5: func() bool { return true }})
	sc := command.NewSaveCoordinator(poster, startNopPublisher{}, 500*time.Millisecond, 5*time.Second, 0, nil)
	cmds := command.NewCommands(command.Deps{Poster: poster, Save: sc, Queries: q, IsOS5: func() bool { return true }})
	srv := newRCIBatchServer(t, &eventLog{})
	o := &OperatorNativeWG{
		transport:    transport.NewWithURL(srv.srv.URL, transport.NewSemaphore(2)),
		commands:     cmds,
		kmod:         km,
		appLog:       logging.NewScopedLogger(nil, logging.GroupTunnel, logging.SubOps),
		supportsASC:  func() bool { return asc },
		supportsASC3: func() bool { return asc3 },
		// resolveFn ОБЯЗАТЕЛЕН: resolveOnce зовёт его в горутине без nil-гарда
		// (operator.go:1163-1165) — nil-func = крэш всего test-binary.
		resolveFn: func(string) (string, int, error) { return "203.0.113.10", 5060, nil },
	}
	// kmod.EnsureLoaded (startProxy) без стабов читает хост: resolveKoPath →
	// ndmsinfo, isLoadedFn → /proc/awg_proxy/version, modLoadedFn → /proc/modules,
	// затем insmod. Стабы делают его детерминированным отказом «kmod: …» —
	// после ResetASCParams, который и есть первый наблюдаемый шаг ветки.
	km.koPath = "/nonexistent/awg_proxy.ko"
	km.isLoadedFn = func() bool { return false }
	km.modLoadedFn = func(string) bool { return true }
	km.execFn = func(context.Context, string, ...string) (*exec.Result, error) {
		return nil, errors.New("insmod: стаб теста")
	}
	return o, stub, poster
}

func nwgStored(iface storage.AWGInterface) *storage.AWGTunnel {
	return &storage.AWGTunnel{ID: "awg0", Name: "n", Backend: "nativewg", NWGIndex: 0,
		Interface: iface,
		Peer:      storage.AWGPeer{PublicKey: "pk", Endpoint: "203.0.113.10:5060", AllowedIPs: []string{"0.0.0.0/0"}}}
}

// Слот awg_proxy принадлежит туннелю: Stop и SuspendProxy обязаны его снять,
// иначе сирота держит порт, ест пул kmodMaxSlots и блокирует апгрейд модуля
// (#702). Наблюдаем запись в /proc/awg_proxy/del через procStub. Возврат
// Stop/SuspendProxy не ассертится: Stop глотает ошибки батча, а нас интересует
// только снятие слота.
func TestStopAndSuspend_RemoveKmodSlot(t *testing.T) {
	for _, tc := range []struct {
		name string
		call func(o *OperatorNativeWG, st *storage.AWGTunnel) error
	}{
		{"Stop", func(o *OperatorNativeWG, st *storage.AWGTunnel) error { return o.Stop(context.Background(), st) }},
		{"SuspendProxy", func(o *OperatorNativeWG, st *storage.AWGTunnel) error {
			return o.SuspendProxy(context.Background(), st)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o, stub, _ := newLifecycleOperator(t, false, false)
			if _, err := o.kmod.AddTunnel("awg0", defaultCfg()); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(stub.listBody, "203.0.113.10:5060") {
				t.Fatalf("слот не появился в /proc/list: %q", stub.listBody)
			}
			_ = tc.call(o, nwgStored(storage.AWGInterface{}))
			var deleted bool
			for _, w := range stub.writes {
				if w.path == "/proc/awg_proxy/del" && strings.TrimSpace(w.body) == "203.0.113.10:5060" {
					deleted = true
				}
			}
			if !deleted {
				t.Fatalf("%s не снял слот kmod: writes=%+v", tc.name, stub.writes)
			}
			if strings.Contains(stub.listBody, "203.0.113.10:5060") {
				t.Fatalf("слот остался в /proc/list: %q", stub.listBody)
			}
		})
	}
}

// Прошивка с ASC 2.0 не знает AWG 3.x: параметры 3.x в NDMS слать нельзя —
// туннель уже идёт через awg_proxy, и ASC поверх дал бы двойную обфускацию
// («живой туннель без единого пакета», PR #819). Для 2.0 — шлём.
func TestSyncAWGParams_SkipsASCForAWG3OnASC2Firmware(t *testing.T) {
	awg20 := storage.AWGInterface{AWGObfuscation: storage.AWGObfuscation{Jc: 4, H1: "10-20", H2: "2", H3: "3", H4: "4"}}
	awg30 := storage.AWGInterface{AWGObfuscation: storage.AWGObfuscation{Jc: 4, H1: "1", H2: "2", H3: "3", H4: "4",
		HeaderProtectionKey: "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="}}

	o, _, poster := newLifecycleOperator(t, true, false)
	if err := o.SyncAWGParams(context.Background(), nwgStored(awg30)); err != nil {
		t.Fatal(err)
	}
	if poster.has(`"asc"`) {
		t.Fatalf("ASC-параметры 3.x ушли в NDMS на прошивке ASC 2.0: %v", poster.list())
	}

	o, _, poster = newLifecycleOperator(t, true, false)
	if err := o.SyncAWGParams(context.Background(), nwgStored(awg20)); err != nil {
		t.Fatal(err)
	}
	if !poster.has(`{"interface":{"Wireguard0":{"wireguard":{"asc":`) {
		t.Fatalf("ASC-параметры 2.0 не ушли в NDMS: %v", poster.list())
	}
}

// Start: конфиг под ASC идёт нативно (SetASCParams в NDMS), конфиг вне ASC —
// через awg_proxy (первым шагом снимает ASC: `no wireguard asc`). Инверсия
// гейта меняет первый наблюдаемый шаг местами.
func TestStart_DispatchesByASCCoverage(t *testing.T) {
	awg20 := storage.AWGInterface{AWGObfuscation: storage.AWGObfuscation{Jc: 4, H1: "10-20", H2: "2", H3: "3", H4: "4"}}
	awg30 := storage.AWGInterface{AWGObfuscation: storage.AWGObfuscation{Jc: 4, H1: "1", H2: "2", H3: "3", H4: "4",
		HeaderProtectionKey: "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="}}
	const reset = `{"parse":"interface Wireguard0 no wireguard asc"}`

	t.Run("2.0 на ASC — нативно", func(t *testing.T) {
		o, _, poster := newLifecycleOperator(t, true, false)
		// Хвост startNative (SyncAddressMTU при пустом Address) может отказать —
		// это Warn, не отказ Start; нас интересует первый шаг.
		_ = o.Start(context.Background(), nwgStored(awg20))
		if !poster.has(`"asc":`) || poster.has(reset) {
			t.Fatalf("2.0 не пошёл нативно: %v", poster.list())
		}
	})
	t.Run("3.0 на ASC 2.0 — прокси", func(t *testing.T) {
		o, _, poster := newLifecycleOperator(t, true, false)
		// Хвост startProxy упирается в kmod.EnsureLoaded — он застаблен
		// детерминированным отказом, ошибка Start здесь допустима.
		_ = o.Start(context.Background(), nwgStored(awg30))
		if !poster.has(reset) || poster.has(`"asc":`) {
			t.Fatalf("3.0 не пошёл через прокси: %v", poster.list())
		}
	})
}
