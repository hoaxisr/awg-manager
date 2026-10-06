package nwg

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms/command"
)

const savePayloadJSON = `{"system":{"configuration":{"save":{}}}}`

// П24: снос NativeWG — командой `no interface` (единый путь, жетон), без
// save в том же пакете. Мутация: вернуть батч [delete, save] → красный по
// форме Posts.
func TestDelete_NoInterface_ViaCommands(t *testing.T) {
	o, _, poster, f, srv := newLifecycleOperator(t, false, false)
	if err := o.Delete(context.Background(), nwgStored(awgObfuscatedIface())); err != nil {
		t.Fatal(err)
	}
	if !poster.has(`{"interface":{"Wireguard0":{"no":true}}}`) || f.Has("Wireguard0") {
		t.Fatalf("снос не командой: posts=%v", poster.list())
	}
	bodies, _ := srv.sent()
	for _, b := range append(bodies, poster.list()...) {
		if strings.Contains(b, `"no":true`) && strings.Contains(b, `"save"`) {
			t.Fatalf("save в пакете со сносом: %s", b)
		}
	}
}

// В3/П24: nwg не шлёт save в батчах — сохранение заказывает координатор
// команд (debounce 0 → отдельный save-POST). Мутация: вернуть save в батч →
// красный.
func TestNwg_SaveViaCoordinator(t *testing.T) {
	ctx := context.Background()
	o, _, poster, f, srv := newLifecycleOperator(t, false, false)
	o.commands = command.NewCommands(command.Deps{Poster: poster, Save: command.NewSaveCoordinator(poster, nil, 0, 0, 0, nil), Queries: o.queries})
	f.ExpectCreate("Wireguard1")
	if _, err := o.createViaBatch(ctx, nwgStored(awgObfuscatedIface())); err != nil {
		t.Fatal(err)
	}
	st := nwgStored(awgObfuscatedIface())
	if err := o.SyncPrivateKey(ctx, ifaceOf(st), st); err != nil {
		t.Fatal(err)
	}
	if err := o.Delete(ctx, st); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for !poster.has(savePayloadJSON) {
		if time.Now().After(deadline) {
			t.Fatalf("координатор не сохранил: posts=%v", poster.list())
		}
		time.Sleep(10 * time.Millisecond)
	}
	bodies, _ := srv.sent()
	if len(bodies) == 0 {
		t.Fatal("батчей нет — тест холостой")
	}
	for _, b := range append(bodies, poster.list()...) {
		if strings.HasPrefix(b, "[") && strings.Contains(b, `"configuration":{"save"`) {
			t.Fatalf("save в батче: %s", b)
		}
	}
}

// Н3: батч применяется поэлементно — отказ элемента не отменяет применённые,
// сохранение заказывается и на ошибке. Мутация: Request только на успехе →
// PendingCount 0, красный.
func TestNwg_BatchError_StillRequestsSave(t *testing.T) {
	o, _, _, _, srv := newLifecycleOperator(t, false, false)
	sc := command.NewSaveCoordinator(nil, nil, time.Hour, time.Hour, 0, nil)
	o.commands.Save = sc
	srv.respond = func(string) (string, bool) {
		return `[{"status":"error","message":"bad key"}]`, true
	}
	st := nwgStored(awgObfuscatedIface())
	if err := o.SyncPrivateKey(context.Background(), ifaceOf(st), st); err == nil {
		t.Fatal("отказ батча принят")
	}
	if got := sc.Status().PendingCount; got != 1 {
		t.Fatalf("PendingCount=%d, ждали 1", got)
	}
}

// П24: создание NativeWG пакетом — командой создания (координатор и кэши из
// конструктора команд), не голым POST транспорта.
func TestNwg_CreateInterface_ViaCommands(t *testing.T) {
	o, _, poster, f, srv := newLifecycleOperator(t, false, false)
	f.ExpectCreate("Wireguard1")
	if _, err := o.createViaBatch(context.Background(), nwgStored(awgObfuscatedIface())); err != nil {
		t.Fatal(err)
	}
	if !poster.has(`[{"interface":{"name":"Wireguard1"}}]`) {
		t.Fatalf("создание не через команды: %v", poster.list())
	}
	bodies, _ := srv.sent()
	for _, b := range bodies {
		if b == `[{"interface":{"name":"Wireguard1"}}]` {
			t.Fatalf("создание голым батчем транспорта: %v", bodies)
		}
	}
}
