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
	sc := command.NewSaveCoordinator(poster, nil, 0, 0, 0, nil)
	sc.SetSaveTimings(command.SaveEventCap, 0, command.SaveAfterRemoval) // без шины событий
	o.commands = command.NewCommands(command.Deps{Poster: poster, Save: sc, Queries: o.queries})
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
	sc.SetSaveTimings(command.SaveEventCap, 0, command.SaveAfterRemoval) // без шины событий
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

// П8/П26: up:false батча Stop — кредит своей грани conf=disabled до POST
// (ExpectConf), отказ батча — кредит назад. Мутации: «без ExpectConf в Stop» →
// успех без кредита, красный; «без refused» → кредит после отказа, красный.
func TestNwg_Stop_ConfCredit(t *testing.T) {
	for _, refuse := range []bool{false, true} {
		o, _, _, _, srv := newLifecycleOperator(t, false, false)
		if refuse {
			srv.respond = func(string) (string, bool) {
				return `[{"status":"error","message":"busy"}]`, true
			}
		}
		_ = o.Stop(context.Background(), nwgStored(awgObfuscatedIface()))
		if got := o.queries.Interfaces.ClaimOwnConf("Wireguard0", "disabled"); got == refuse {
			t.Fatalf("refuse=%v: кредит conf=disabled = %v", refuse, got)
		}
	}
}

// R68-2: NDMS применяет батч поэлементно — кредит грани conf снимается, только
// если отказал сам `up` или ответа нет (применение неизвестно); отказ соседнего
// элемента кредит оставляет. Мутация «снимать при любой ошибке батча» →
// «отказал сосед» без кредита, красный; «никогда не снимать» → «отказал up»
// с кредитом, красный.
func TestNwg_PostUpBatch_CreditByUpElement(t *testing.T) {
	for _, tc := range []struct {
		name, resp string
		keep       bool
	}{
		{"отказал сосед", `[{"status":"error","message":"bad endpoint"},{}]`, true},
		{"отказал up", `[{},{"status":"error","message":"busy"}]`, false},
		{"ответ не разобран", `not json`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o, _, _, _, srv := newLifecycleOperator(t, false, false)
			ctx := context.Background()
			iface, _, ok, err := o.queries.Interfaces.Confirm(ctx, "Wireguard0")
			if err != nil || !ok {
				t.Fatalf("confirm: ok=%v err=%v", ok, err)
			}
			srv.respond = func(string) (string, bool) { return tc.resp, true }
			if _, err := o.postUpBatch(ctx, iface, true, []any{map[string]any{"x": 1}}); err == nil {
				t.Fatal("отказ батча принят")
			}
			if got := o.queries.Interfaces.ClaimOwnConf("Wireguard0", "running"); got != tc.keep {
				t.Fatalf("кредит running = %v, want %v", got, tc.keep)
			}
		})
	}
}
