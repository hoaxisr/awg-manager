package api

import (
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/ndms/events"
	"github.com/hoaxisr/awg-manager/internal/orchestrator"
)

type spyDispatcher struct {
	mu     sync.Mutex
	events []events.Event
}

func (s *spyDispatcher) Enqueue(e events.Event) {
	s.mu.Lock()
	s.events = append(s.events, e)
	s.mu.Unlock()
}

func (s *spyDispatcher) Events() []events.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]events.Event, len(s.events))
	copy(out, s.events)
	return out
}

// handleForm подаёт строку spool так же, как SpoolReader: ParseHookForm → Handle.
func handleForm(t *testing.T, h *HookHandler, form string) {
	t.Helper()
	v, err := url.ParseQuery(form)
	if err != nil {
		t.Fatal(err)
	}
	ev, err := events.ParseHookForm(v)
	if err != nil {
		t.Fatal(err)
	}
	h.Handle(ev)
}

func newTestHookHandler(disp HookDispatcher) *HookHandler {
	return &HookHandler{
		dispatcher: disp,
		log:        logging.NewScopedLogger(nil, logging.GroupSystem, logging.SubBoot),
	}
}

func TestHookHandler_Handle_LayerChanged(t *testing.T) {
	disp := &spyDispatcher{}
	h := newTestHookHandler(disp)

	handleForm(t, h, "type=iflayerchanged&id=Wireguard0&layer=conf&level=running")

	got := disp.Events()
	if len(got) != 1 {
		t.Fatalf("events: want 1, got %d", len(got))
	}
	if got[0].Type != events.EventIfLayerChanged || got[0].ID != "Wireguard0" ||
		got[0].Layer != "conf" || got[0].Level != "running" {
		t.Errorf("event: %#v", got[0])
	}
}

func TestHookHandler_Handle_IfCreated(t *testing.T) {
	disp := &spyDispatcher{}
	h := newTestHookHandler(disp)

	handleForm(t, h, "type=ifcreated&id=Wireguard1&system_name=nwg1")
	got := disp.Events()
	if len(got) != 1 || got[0].Type != events.EventIfCreated {
		t.Errorf("event: %#v", got)
	}
}

// Своё создание помечается в событии: диспетчер сверит его списком, но
// публиковать не станет — создатель публикует сам после записи в стор.
func TestHookHandler_IfCreated_UnderGate_SelfCreated(t *testing.T) {
	disp := &spyDispatcher{}
	h := newTestHookHandler(disp)

	h.EnterSelfCreate()
	handleForm(t, h, "type=ifcreated&id=Wireguard1")
	handleForm(t, h, "type=ifdestroyed&id=Wireguard2")
	h.ExitSelfCreate()
	handleForm(t, h, "type=ifcreated&id=Wireguard3")

	got := disp.Events()
	if len(got) != 3 {
		t.Fatalf("events: want 3, got %d", len(got))
	}
	if !got[0].SelfCreated {
		t.Errorf("ifcreated под гейтом: SelfCreated=false, want true")
	}
	if got[1].SelfCreated {
		t.Errorf("ifdestroyed под гейтом: SelfCreated=true, want false")
	}
	if got[2].SelfCreated {
		t.Errorf("ifcreated после ExitSelfCreate: SelfCreated=true, want false")
	}
}

func TestHookHandler_Handle_NilDispatcher_NoPanic(t *testing.T) {
	h := newTestHookHandler(nil) // nil dispatcher

	// Should not panic even if dispatcher is nil.
	handleForm(t, h, "type=ifcreated&id=X")
}

// fakeWANModel records SetUp calls for the ipv4-layer hook path.
type fakeWANModel struct {
	mu    sync.Mutex
	calls []fakeWANSetUp
}

type fakeWANSetUp struct {
	Name string
	Up   bool
}

func (f *fakeWANModel) SetUp(name string, up bool) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fakeWANSetUp{Name: name, Up: up})
	return true
}

func (f *fakeWANModel) Calls() []fakeWANSetUp {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]fakeWANSetUp, len(f.calls))
	copy(out, f.calls)
	return out
}

func TestHookHandler_Handle_IPv4Up_NudgesProxyRuntime(t *testing.T) {
	h := newTestHookHandler(&spyDispatcher{})
	h.SetWANModel(&fakeWANModel{})
	done := make(chan string, 1)
	h.SetProxyRuntimeNudge(func(reason string) {
		done <- reason
	})

	handleForm(t, h, "type=iflayerchanged&id=PPPoE0&system_name=ppp0&layer=ipv4&level=running")

	select {
	case reason := <-done:
		if reason != "wan-up" {
			t.Fatalf("proxy runtime nudge reason: want wan-up, got %q", reason)
		}
	case <-time.After(time.Second):
		t.Fatal("proxy runtime nudge not invoked on WAN up")
	}
}

func TestHookHandler_Handle_IPv4Up_UpdatesWANModel(t *testing.T) {
	disp := &spyDispatcher{}
	h := newTestHookHandler(disp)
	wm := &fakeWANModel{}
	h.SetWANModel(wm)

	handleForm(t, h, "type=iflayerchanged&id=PPPoE0&system_name=ppp0&layer=ipv4&level=running")

	// Dispatcher still sees the event (cache invalidation is independent).
	if got := disp.Events(); len(got) != 1 || got[0].Type != events.EventIfLayerChanged {
		t.Errorf("dispatcher: %#v", got)
	}

	calls := wm.Calls()
	if len(calls) != 1 {
		t.Fatalf("wan SetUp calls: want 1, got %d (%#v)", len(calls), calls)
	}
	if calls[0].Name != "ppp0" || !calls[0].Up {
		t.Errorf("wan SetUp: want (ppp0, true), got %#v", calls[0])
	}
}

func TestHookHandler_Handle_IPv4Down_EmitsWANDown(t *testing.T) {
	disp := &spyDispatcher{}
	h := newTestHookHandler(disp)
	wm := &fakeWANModel{}
	h.SetWANModel(wm)

	handleForm(t, h, "type=iflayerchanged&id=PPPoE0&system_name=ppp0&layer=ipv4&level=disabled")

	calls := wm.Calls()
	if len(calls) != 1 {
		t.Fatalf("wan SetUp calls: want 1, got %d", len(calls))
	}
	if calls[0].Name != "ppp0" || calls[0].Up {
		t.Errorf("wan SetUp: want (ppp0, false), got %#v", calls[0])
	}
}

func TestHookHandler_Handle_IPv4_VPNInterface_Skipped(t *testing.T) {
	disp := &spyDispatcher{}
	h := newTestHookHandler(disp)
	wm := &fakeWANModel{}
	h.SetWANModel(wm)

	// nwg0 matches the IsNonISPInterface filter — must NOT touch WAN model.
	handleForm(t, h, "type=iflayerchanged&id=Wireguard0&system_name=nwg0&layer=ipv4&level=running")

	if calls := wm.Calls(); len(calls) != 0 {
		t.Errorf("wan SetUp: want 0 (VPN filtered), got %#v", calls)
	}
	// Dispatcher still sees the event — cache invalidation is independent of filter.
	if got := disp.Events(); len(got) != 1 {
		t.Errorf("dispatcher: want 1, got %d", len(got))
	}
}

func TestHookHandler_Handle_IPv4_EmptySystemName_Skipped(t *testing.T) {
	disp := &spyDispatcher{}
	h := newTestHookHandler(disp)
	wm := &fakeWANModel{}
	h.SetWANModel(wm)

	// No system_name → no kernel name → cannot update WAN model.
	handleForm(t, h, "type=iflayerchanged&id=PPPoE0&layer=ipv4&level=running")
	if calls := wm.Calls(); len(calls) != 0 {
		t.Errorf("wan SetUp: want 0 (empty system_name), got %#v", calls)
	}
}

// F118: смена адреса WAN (DHCP lease, IPCP у PPPoE) — повод перепроверить
// DDNS-имена, за которыми следит страж, не дожидаясь его тика.
func TestHookHandler_IPChanged_NudgesEndpointGuard(t *testing.T) {
	h := newTestHookHandler(&spyDispatcher{})
	nudged := make(chan struct{}, 1)
	h.SetEndpointGuardNudge(func() { nudged <- struct{}{} })

	handleForm(t, h, "type=ifipchanged&id=PPPoE0&address=203.0.113.9")

	select {
	case <-nudged:
	case <-time.After(2 * time.Second):
		t.Fatal("страж не разбужен на смену адреса WAN")
	}
}

// Прочие хуки стража не будят: он ходит в DNS, и дёргать его на каждом
// событии интерфейса незачем.
func TestHookHandler_LayerChanged_DoesNotNudgeGuard(t *testing.T) {
	h := newTestHookHandler(&spyDispatcher{})
	nudged := make(chan struct{}, 1)
	h.SetEndpointGuardNudge(func() { nudged <- struct{}{} })

	handleForm(t, h, "type=iflayerchanged&id=Wireguard0&layer=conf&level=running")

	select {
	case <-nudged:
		t.Fatal("страж разбужен не своим событием")
	case <-time.After(200 * time.Millisecond):
	}
}

// F497: клиентские маршруты на system:-выходе теряются на down/up
// интерфейса — ядро снимает default dev. Хук ipv4 running обязан
// переприменить их для ЛЮБОГО интерфейса, не только ISP.
func TestHookHandler_IPv4Running_CallsHookForAnyInterface(t *testing.T) {
	h := newTestHookHandler(&spyDispatcher{})
	got := make(chan string, 1)
	h.SetIPv4RunningHook(func(id string) { got <- id })

	handleForm(t, h, "type=iflayerchanged&id=OpkgTun7&system_name=opkgtun7&layer=ipv4&level=running")

	select {
	case id := <-got:
		if id != "OpkgTun7" {
			t.Fatalf("id = %q", id)
		}
	case <-time.After(time.Second):
		t.Fatal("хук ipv4 running не вызван для OpkgTun7")
	}
}

func TestHookHandler_IPv4Disabled_NoHook(t *testing.T) {
	h := newTestHookHandler(&spyDispatcher{})
	fired := make(chan struct{}, 1)
	h.SetIPv4RunningHook(func(string) { fired <- struct{}{} })
	handleForm(t, h, "type=iflayerchanged&id=OpkgTun7&system_name=opkgtun7&layer=ipv4&level=disabled")

	select {
	case <-fired:
		t.Fatal("хук вызван на disabled")
	case <-time.After(200 * time.Millisecond):
	}
}

// captureAppLogger собирает записи журнала — по ним видно, что событие дошло
// до оркестратора.
type captureAppLogger struct {
	mu    sync.Mutex
	lines []string
}

func (c *captureAppLogger) AppLog(_ logging.Level, _, _, action, target, message string) {
	c.mu.Lock()
	c.lines = append(c.lines, action+"|"+target+"|"+message)
	c.mu.Unlock()
}

func (c *captureAppLogger) has(sub string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, l := range c.lines {
		if strings.Contains(l, sub) {
			return true
		}
	}
	return false
}

// Handle — единственная точка входа события без HTTP: в диспетчер и, для
// iflayerchanged conf, в оркестратор. Доход до оркестратора виден по
// поглощённому ожиданию хука — оно не требует ни хранилища, ни операторов.
func TestHandle_DirectCall_EnqueuesAndForwards(t *testing.T) {
	disp := &spyDispatcher{}
	h := newTestHookHandler(disp)
	logs := &captureAppLogger{}
	orch := orchestrator.New(nil, nil, nil, nil, nil, logs)
	orch.ExpectHook("Wireguard0", "running")
	h.orch = orch

	ev := events.Event{Type: events.EventIfLayerChanged, ID: "Wireguard0", Layer: "conf", Level: "running"}
	h.Handle(ev)

	if got := disp.Events(); len(got) != 1 || got[0] != ev {
		t.Fatalf("dispatcher: %#v", got)
	}
	deadline := time.Now().Add(2 * time.Second)
	for !logs.has("expected-hook consumed") {
		if time.Now().After(deadline) {
			t.Fatal("событие не дошло до оркестратора")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// До публикации готового обработчика событие попадает только в диспетчер
// (кэш), оркестратор его не видит; после публикации — и туда, и туда.
func TestHookSink_BeforeAndAfterPublish(t *testing.T) {
	disp := &spyDispatcher{}
	logs := &captureAppLogger{}
	orch := orchestrator.New(nil, nil, nil, nil, nil, logs)
	orch.ExpectHook("Wireguard0", "running")
	orch.ExpectHook("Wireguard0", "running")
	ev := events.Event{Type: events.EventIfLayerChanged, ID: "Wireguard0", Layer: "conf", Level: "running"}

	sink := NewHookSink(disp)
	sink.Handle(ev)
	if got := disp.Events(); len(got) != 1 || got[0] != ev {
		t.Fatalf("до публикации, диспетчер: %#v", got)
	}
	time.Sleep(100 * time.Millisecond)
	if logs.has("expected-hook consumed") {
		t.Fatal("до публикации событие дошло до оркестратора")
	}

	h := newTestHookHandler(disp)
	h.orch = orch
	sink.Publish(h)
	sink.Handle(ev)
	if got := disp.Events(); len(got) != 2 {
		t.Fatalf("после публикации, диспетчер: %d событий", len(got))
	}
	deadline := time.Now().Add(2 * time.Second)
	for !logs.has("expected-hook consumed") {
		if time.Now().After(deadline) {
			t.Fatal("после публикации событие не дошло до оркестратора")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// F569: ifdestroyed доходит до оркестратора (явная реакция на снятие нашей
// записи не зависит от layer-хуков, #328). Доход виден по поглощённому
// ожиданию "destroyed".
func TestHandle_IfDestroyed_ForwardsToOrchestrator(t *testing.T) {
	disp := &spyDispatcher{}
	h := newTestHookHandler(disp)
	logs := &captureAppLogger{}
	orch := orchestrator.New(nil, nil, nil, nil, nil, logs)
	orch.ExpectHook("OpkgTun10", "destroyed")
	h.orch = orch

	h.Handle(events.Event{Type: events.EventIfDestroyed, ID: "OpkgTun10"})

	if got := disp.Events(); len(got) != 1 || got[0].Type != events.EventIfDestroyed {
		t.Fatalf("dispatcher: %#v", got)
	}
	deadline := time.Now().Add(2 * time.Second)
	for !logs.has("expected-hook consumed level=destroyed") {
		if time.Now().After(deadline) {
			t.Fatal("ifdestroyed не дошёл до оркестратора")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// В1 (F595): t= хук-скрипта даёт Debug «hook age» — разность аптаймов,
// округлённая до секунды. Без t= (или без аптайма) строки нет.
func TestHandle_ScriptUptime_LogsHookAge(t *testing.T) {
	cases := []struct {
		name   string
		event  events.Event
		uptime float64
		want   string
	}{
		{"age", events.Event{Type: events.EventIfCreated, ID: "Wireguard1", ScriptUptime: 100}, 122.4, "hook age=22s"},
		{"round", events.Event{Type: events.EventIfCreated, ID: "Wireguard1", ScriptUptime: 100}, 122.6, "hook age=23s"},
		{"no t", events.Event{Type: events.EventIfCreated, ID: "Wireguard1"}, 122.4, ""},
		{"no uptime", events.Event{Type: events.EventIfCreated, ID: "Wireguard1", ScriptUptime: 100}, 0, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			logs := &captureAppLogger{}
			h := &HookHandler{log: logging.NewScopedLogger(logs, logging.GroupSystem, logging.SubBoot)}
			h.SetUptimeReader(func() float64 { return c.uptime })
			h.Handle(c.event)
			if c.want != "" {
				if !logs.has("hook|Wireguard1|" + c.want) {
					t.Fatalf("нет %q в журнале: %q", c.want, logs.lines)
				}
				return
			}
			if logs.has("hook age") {
				t.Fatalf("лишняя строка age: %q", logs.lines)
			}
		})
	}
}
