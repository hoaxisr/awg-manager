package query

import (
	"context"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/ndms"
)

// F570: имя ядра читается из памяти — таблица классов, хуки, резолвер вслед
// за списком. Вызовы ResolveSystemName/SystemNames/ListAll/ListWAN в NDMS не
// ходят никогда: по снятому, но ещё числящемуся в кэше имени NDMS пишет E.

// Wireguard — класс с детерминированным именем: эхо id в списке резолвер не
// зовёт ни при списке, ни по требованию.
func TestResolve_DeterministicClass_NoRCI(t *testing.T) {
	f := NewFakeNDMS(ndms.Interface{ID: "Wireguard1", Type: "Wireguard", SystemName: "Wireguard1"})
	q := NewQueries(Deps{Getter: f, Logger: NopLogger()})
	ctx := context.Background()
	if got := q.Interfaces.ResolveSystemName(ctx, "Wireguard1"); got != "nwg1" {
		t.Fatalf("ResolveSystemName = %q, want nwg1", got)
	}
	if got := q.Interfaces.SystemNames(ctx, []string{"Wireguard1"}); got["Wireguard1"] != "nwg1" {
		t.Fatalf("SystemNames = %v, want nwg1", got)
	}
	if len(f.Posts) != 0 {
		t.Fatalf("POST = %v, want 0", f.Posts)
	}
}

// Снят после списка, хук не доставлен (F571: секунды) — ни одного POST, E == 0.
func TestResolve_RemovedAfterList_NoRCI(t *testing.T) {
	f := NewFakeNDMS(
		ndms.Interface{ID: "Wireguard1", Type: "Wireguard", SystemName: "Wireguard1"},
		ndms.Interface{ID: "GigabitEthernet1", Type: "GigabitEthernet", SystemName: "eth3", SecurityLevel: "public"},
		ndms.Interface{ID: "Bridge0", Type: "Bridge", SystemName: "br0"},
		// Имени не дал ни список, ни резолвер вслед за ним.
		ndms.Interface{ID: "UsbQmi1", Type: "UsbQmi", SecurityLevel: "public"},
	)
	q := NewQueries(Deps{Getter: f, Logger: NopLogger()})
	ctx := context.Background()
	_, _ = q.Interfaces.List(ctx) // bootstrap
	posts := len(f.Posts)
	ids := []string{"Wireguard1", "GigabitEthernet1", "UsbQmi1", "Wireguard7", "UsbQmi0"}
	for _, id := range ids[:3] {
		f.Remove(id)
	}
	_ = f.DrainHooks() // хуки не доставлены

	for _, id := range ids {
		_ = q.Interfaces.ResolveSystemName(ctx, id)
	}
	_ = q.Interfaces.SystemNames(ctx, ids)
	_, _ = q.Interfaces.ListAll(ctx)
	_, _ = q.Interfaces.ListWAN(ctx)
	if got := len(f.Posts) - posts; got != 0 {
		t.Fatalf("POST после списка = %d (%v), want 0", got, f.Posts[posts:])
	}
	if f.E != 0 {
		t.Fatalf("E = %d, want 0", f.E)
	}
}

// Недетерминированный класс с эхом в списке: резолвер один раз вслед за
// первым списком; следующий список (Confirm) и ListAll имя уже знают.
func TestRefreshList_ResolvesUnknownOnce(t *testing.T) {
	fg := newFakeGetter()
	fg.SetJSON(ifaceListPath, `{
		"GigabitEthernet1": {"id":"GigabitEthernet1","interface-name":"GigabitEthernet1","type":"GigabitEthernet","state":"up","security-level":"public"}
	}`)
	fg.SetPostSystemName("GigabitEthernet1", `"eth3"`)
	s := NewInterfaceStore(fg, NopLogger())
	ctx := context.Background()

	if _, err := s.List(ctx); err != nil {
		t.Fatal(err)
	}
	if got := fg.PostSystemNameCalls("GigabitEthernet1"); got != 1 {
		t.Fatalf("резолвер после первого списка: %d вызовов, want 1", got)
	}
	if _, _, _, err := s.Confirm(ctx, "GigabitEthernet1"); err != nil {
		t.Fatal(err)
	}
	if got := s.ResolveSystemName(ctx, "GigabitEthernet1"); got != "eth3" {
		t.Fatalf("ResolveSystemName = %q, want eth3", got)
	}
	if got := fg.PostSystemNameCalls("GigabitEthernet1"); got != 1 {
		t.Fatalf("резолвер после второго списка: %d вызовов, want 1", got)
	}
}

// Имя из хука (system_name) закрывает id без резолвера.
func TestOnSystemName_FromHook(t *testing.T) {
	fg := newFakeGetter()
	fg.SetJSON(ifaceListPath, `{
		"UsbQmi0": {"id":"UsbQmi0","type":"UsbQmi","state":"up","security-level":"public"}
	}`)
	s := NewInterfaceStore(fg, NopLogger())
	s.OnSystemName("UsbQmi0", "usb0")
	ctx := context.Background()
	if got := s.ResolveSystemName(ctx, "UsbQmi0"); got != "usb0" {
		t.Fatalf("ResolveSystemName = %q, want usb0", got)
	}
	if got := fg.PostSystemNameCalls("UsbQmi0"); got != 0 {
		t.Fatalf("резолвер: %d вызовов, want 0 — имя пришло хуком", got)
	}
}

// ListAll после bootstrap своего резолвера не зовёт.
func TestListAll_NoOwnResolver(t *testing.T) {
	fg := newFakeGetter()
	fg.SetJSON(ifaceListPath, `{
		"GigabitEthernet1": {"id":"GigabitEthernet1","type":"GigabitEthernet","state":"up","security-level":"public"},
		"UsbQmi0": {"id":"UsbQmi0","type":"UsbQmi","state":"up","security-level":"public"}
	}`)
	fg.SetPostSystemName("GigabitEthernet1", `"eth3"`)
	fg.SetPostSystemName("UsbQmi0", `"usb0"`)
	s := NewInterfaceStore(fg, NopLogger())
	ctx := context.Background()
	_, _ = s.List(ctx) // bootstrap
	before := fg.PostSystemNameCalls("GigabitEthernet1") + fg.PostSystemNameCalls("UsbQmi0")
	all, err := s.ListAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = s.ListWAN(ctx)
	if got := fg.PostSystemNameCalls("GigabitEthernet1") + fg.PostSystemNameCalls("UsbQmi0") - before; got != 0 {
		t.Fatalf("ListAll/ListWAN резолвили сами: %d вызовов, want 0", got)
	}
	if len(all) != 2 || all[0].Name != "eth3" || all[1].Name != "usb0" {
		t.Fatalf("ListAll = %+v, want eth3, usb0", all)
	}
}

// Эхо id из хука в карту имён не ложится: иначе id снят с резолвера вслед за
// списком навсегда.
func TestOnSystemName_EchoDoesNotBlockResolver(t *testing.T) {
	fg := newFakeGetter()
	fg.SetJSON(ifaceListPath, `{
		"UsbQmi0": {"id":"UsbQmi0","type":"UsbQmi","state":"up"}
	}`)
	fg.SetPostSystemName("UsbQmi0", `"usb0"`)
	s := NewInterfaceStore(fg, NopLogger())
	s.OnSystemName("UsbQmi0", "UsbQmi0")
	if got := s.ResolveSystemName(context.Background(), "UsbQmi0"); got != "usb0" {
		t.Fatalf("ResolveSystemName = %q, want usb0 от резолвера вслед за списком", got)
	}
	if got := fg.PostSystemNameCalls("UsbQmi0"); got != 1 {
		t.Fatalf("резолвер: %d вызовов, want 1", got)
	}
}

// Пустой ответ резолвера запоминается: следующий список (Confirm) id без
// имени не переспрашивает — иначе POST на каждой мутации.
func TestRefreshList_EmptyAnswerNotReasked(t *testing.T) {
	fg := newFakeGetter()
	fg.SetJSON(ifaceListPath, `{
		"UsbQmi0": {"id":"UsbQmi0","type":"UsbQmi","state":"up"}
	}`)
	fg.SetPostSystemName("UsbQmi0", `""`)
	s := NewInterfaceStore(fg, NopLogger())
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if _, _, _, err := s.Confirm(ctx, "UsbQmi0"); err != nil {
			t.Fatal(err)
		}
	}
	if got := fg.PostSystemNameCalls("UsbQmi0"); got != 1 {
		t.Fatalf("резолвер: %d вызовов за два списка, want 1", got)
	}
}

// id, снятый хуком, пока список в полёте, резолвер вслед за этим списком не
// спрашивает: запись уже снята, вопрос по имени — E.
func TestRefreshList_SkipsIDDestroyedInFlight(t *testing.T) {
	f := NewFakeNDMS(ndms.Interface{ID: "UsbQmi1", Type: "UsbQmi"})
	q := NewQueries(Deps{Getter: f, Logger: NopLogger()})
	f.InList(func() {
		f.InList(nil)
		f.Remove("UsbQmi1")
		q.Interfaces.OnDestroyed("UsbQmi1")
	})
	_, _ = q.Interfaces.List(context.Background()) // bootstrap
	for _, p := range f.Posts {
		if strings.Contains(p, "UsbQmi1") {
			t.Fatalf("снятый в полёте id спрошен: %s", p)
		}
	}
	if f.E != 0 {
		t.Fatalf("E = %d, want 0", f.E)
	}
}
