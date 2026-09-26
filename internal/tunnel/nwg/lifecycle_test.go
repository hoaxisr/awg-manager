package nwg

import (
	"context"
	"encoding/base64"
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
	t.Cleanup(o.Close)
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

// Туннель 3.x переехал с awg_proxy на нативный ASC 3.x (обновление прошивки
// или пакета): слот-сирота в ядре, карта менеджера пуста. startNative на
// ASC3-прошивке его снимает; на ASC2 — не трогает (там легитимны proxy-слоты
// соседних туннелей 3.x).
func TestStartNative_DropsOrphanSlotOnASC3(t *testing.T) {
	awg30 := storage.AWGInterface{AWGObfuscation: storage.AWGObfuscation{Jc: 4, H1: "1", H2: "2", H3: "3", H4: "4",
		HeaderProtectionKey: "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="}}
	awg20 := storage.AWGInterface{AWGObfuscation: storage.AWGObfuscation{Jc: 4, H1: "10-20", H2: "2", H3: "3", H4: "4"}}
	for _, tc := range []struct {
		name  string
		asc3  bool
		iface storage.AWGInterface
		want  int
	}{
		{"asc3", true, awg30, 1},
		{"asc2", false, awg20, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o, stub, _ := newLifecycleOperator(t, true, tc.asc3)
			stub.setListSlot("203.0.113.10", 5060, 51820)
			_ = o.Start(context.Background(), nwgStored(tc.iface))
			if got := stub.countWritesTo("/proc/awg_proxy/del"); got != tc.want {
				t.Fatalf("снятий слота %d, ждали %d: %+v", got, tc.want, stub.writes)
			}
		})
	}
}

// Импорт несёт параметры 3.x ровно тогда, когда туннель идёт нативно (ASC3):
// без ASC3 они дали бы строки W на каждый импорт, с ASC3 без них туннель
// встал бы как 2.0.
func TestCreateViaImport_AWG3ParamsFollowASC3(t *testing.T) {
	for _, asc3 := range []bool{false, true} {
		o, _, poster := newLifecycleOperator(t, true, asc3)
		_, _ = o.createViaImport(context.Background(), awg3Tunnel())
		var conf string
		for _, p := range poster.list() {
			var body struct {
				Interface struct {
					Wireguard struct {
						Import string `json:"import"`
					} `json:"wireguard"`
				} `json:"interface"`
			}
			if json.Unmarshal([]byte(p), &body) == nil && body.Interface.Wireguard.Import != "" {
				raw, _ := base64.StdEncoding.DecodeString(body.Interface.Wireguard.Import)
				conf = string(raw)
			}
		}
		if conf == "" {
			t.Fatalf("asc3=%v: импорт не отправлен: %v", asc3, poster.list())
		}
		if got := strings.Contains(conf, "HeaderProtectionKey"); got != asc3 {
			t.Errorf("asc3=%v: HeaderProtectionKey в импорте = %v:\n%s", asc3, got, conf)
		}
	}
}

// На ASC3-прошивке старт и синхронизация туннеля 3.x обязаны отправить ASC с
// параметрами 3.x: без них прошивка поднимет его как 2.0 против сервера 3.1,
// и туннель молча не заработает. Создание на ASC3 идёт импортом (см.
// TestCreateViaImport_AWG3ParamsFollowASC3); createViaBatch — путь прошивок
// до 5.01.A.3, ASC3 там не бывает.
func TestASC3PayloadReachesNDMS(t *testing.T) {
	iface := awg3Tunnel().Interface
	for _, tc := range []struct {
		name string
		call func(o *OperatorNativeWG, st *storage.AWGTunnel)
	}{
		{"Start", func(o *OperatorNativeWG, st *storage.AWGTunnel) { _ = o.Start(context.Background(), st) }},
		{"SyncAWGParams", func(o *OperatorNativeWG, st *storage.AWGTunnel) { _ = o.SyncAWGParams(context.Background(), st) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o, _, poster := newLifecycleOperator(t, true, true)
			tc.call(o, nwgStored(iface))
			if !poster.has(`"header-protection-key":"YWJjZGVmZ2hpamtsbW5vcHFyc3R1dnd4eXoxMjM0NTY="`) {
				t.Fatalf("ASC 3.x не отправлен: %v", poster.list())
			}
		})
	}
}

// Не собрался ASC (мусор в поле 3.x, сохранённый до валидации) — старт
// падает, а не поднимает туннель с прежним ASC из NDMS.
func TestStartNative_FailsOnBadASC(t *testing.T) {
	iface := awg3Tunnel().Interface
	iface.RekeyAfterTime = "abc"
	o, _, _ := newLifecycleOperator(t, true, true)
	if err := o.Start(context.Background(), nwgStored(iface)); err == nil || !strings.Contains(err.Error(), "RekeyAfterTime") {
		t.Fatalf("err = %v", err)
	}
}
