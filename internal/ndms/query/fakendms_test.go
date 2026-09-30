package query

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/ndms/transport"
)

func TestFakeNDMS_PointReadAbsentCountsE(t *testing.T) {
	f := NewFakeNDMS(ndms.Interface{ID: "Wireguard0", Type: "Wireguard", SystemName: "nwg0"})
	raw, err := f.Post(context.Background(), showByName("Wireguard7"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"6553619"`) || f.E != 1 {
		t.Fatalf("want unable-to-find envelope and E=1, got %s E=%d", raw, f.E)
	}
	if _, err := f.Post(context.Background(), showByName("Wireguard0")); err != nil || f.E != 1 {
		t.Fatalf("present read must not count: err=%v E=%d", err, f.E)
	}
}

func TestFakeNDMS_CommandOnAbsentCreatesPhantom(t *testing.T) {
	f := NewFakeNDMS()
	_, _ = f.Post(context.Background(), map[string]any{"interface": map[string]any{"Wireguard2": map[string]any{"up": false}}})
	if f.Phantoms != 1 || !f.Has("Wireguard2") {
		t.Fatalf("phantom expected: Phantoms=%d has=%v", f.Phantoms, f.Has("Wireguard2"))
	}
	hooks := f.DrainHooks()
	layer := FakeHook{Type: "iflayerchanged", ID: "Wireguard2", Layer: "ctrl"}
	if len(hooks) != 3 || hooks[0] != layer || hooks[1] != layer || hooks[2] != (FakeHook{Type: "ifcreated", ID: "Wireguard2"}) {
		t.Fatalf("hooks: %+v", hooks)
	}
	_, _ = f.Post(context.Background(), map[string]any{"interface": map[string]any{"Wireguard2": map[string]any{"no": true}}})
	if f.Has("Wireguard2") || f.DrainHooks()[0].Type != "ifdestroyed" {
		t.Fatal("no interface must remove and queue ifdestroyed")
	}
}

func TestFakeNDMS_CrossSectionRefAbsentCountsE(t *testing.T) {
	f := NewFakeNDMS()
	raw, _ := f.Post(context.Background(), map[string]any{"ip": map[string]any{"route": map[string]any{"default": true, "interface": "OpkgTun12"}}})
	if f.E != 1 || !strings.Contains(string(raw), "no such interface") {
		t.Fatalf("E=%d raw=%s", f.E, raw)
	}
	_, _ = f.Post(context.Background(), map[string]any{"dns-proxy": map[string]any{"route": []any{map[string]any{"group": "AWG_x", "interface": "Wireguard7", "auto": true}}}})
	if f.E != 2 {
		t.Fatalf("E=%d", f.E)
	}
}

func TestFakeNDMS_ImportCreatesWireguard(t *testing.T) {
	f := NewFakeNDMS()
	raw, err := f.Post(context.Background(), map[string]any{"interface": map[string]any{"wireguard": map[string]any{"import": "x", "name": "", "filename": "a.conf"}}})
	if err != nil || !strings.Contains(string(raw), `"created":"Wireguard0"`) || !f.Has("Wireguard0") || f.Phantoms != 0 {
		t.Fatalf("import: err=%v raw=%s phantoms=%d", err, raw, f.Phantoms)
	}
}

func TestFakeNDMS_RCAbsentIs404(t *testing.T) {
	f := NewFakeNDMS()
	var dst map[string]any
	err := f.Get(context.Background(), "/show/rc/interface/Wireguard7", &dst)
	var he *transport.HTTPError
	if !errors.As(err, &he) || he.Status != 404 || f.E != 1 {
		t.Fatalf("err=%v E=%d", err, f.E)
	}
}

// Форма create OpkgTun без ExpectCreate — фантом; с ExpectCreate — намеренное
// создание; импорт — всегда намеренное. Remove кладёт ifdestroyed; список
// читается InterfaceStore, считается и отказывает по FailList.
func TestFakeNDMS_ExpectCreateRemoveAndList(t *testing.T) {
	ctx := context.Background()
	createOpkgTun := func(name string) map[string]any {
		return map[string]any{"interface": map[string]any{name: map[string]any{
			"description": "d", "security-level": map[string]any{"public": true}}}}
	}
	f := NewFakeNDMS(ndms.Interface{ID: "Wireguard0", Type: "Wireguard", SystemName: "nwg0"})
	_, _ = f.Post(ctx, createOpkgTun("OpkgTun3"))
	if f.Phantoms != 1 || len(f.Created) != 0 {
		t.Fatalf("без ExpectCreate: Phantoms=%d Created=%v", f.Phantoms, f.Created)
	}
	f = NewFakeNDMS(ndms.Interface{ID: "Wireguard0", Type: "Wireguard", SystemName: "nwg0"})
	f.ExpectCreate("OpkgTun10")
	_, _ = f.Post(ctx, createOpkgTun("OpkgTun10"))
	_, _ = f.Post(ctx, map[string]any{"interface": map[string]any{"wireguard": map[string]any{"import": "x"}}})
	if f.Phantoms != 0 || len(f.Created) != 2 || f.Created[0] != "OpkgTun10" || f.Created[1] != "Wireguard1" {
		t.Fatalf("ExpectCreate/import: Phantoms=%d Created=%v", f.Phantoms, f.Created)
	}
	f.DrainHooks()
	f.Remove("Wireguard0")
	if hooks := f.DrainHooks(); len(hooks) != 1 || hooks[0] != (FakeHook{Type: "ifdestroyed", ID: "Wireguard0"}) {
		t.Fatalf("hooks: %+v", hooks)
	}

	s := NewInterfaceStore(f, NopLogger())
	list, err := s.List(ctx)
	if err != nil || len(list) != 2 || f.ListCalls() != 1 {
		t.Fatalf("list=%+v err=%v calls=%d", list, err, f.ListCalls())
	}
	if iface, _ := s.Get(ctx, "OpkgTun10"); iface == nil || iface.Type != "OpkgTun" {
		t.Fatalf("OpkgTun10 из списка: %+v", iface)
	}
	f.FailList(errors.New("rci down"))
	if _, err := f.Post(ctx, map[string]any{"show": map[string]any{"interface": map[string]any{}}}); err == nil || f.ListCalls() != 2 {
		t.Fatalf("FailList: err=%v calls=%d", err, f.ListCalls())
	}
}

// Ветки post, на которые опираются сценарии: формы interface/parse, `no` по
// отсутствующему (отказ и E, стенд 5.01.C.6), system-name, пакет, to-interface.
func TestFakeNDMS_PostBranches(t *testing.T) {
	iface := func(name string, body map[string]any) map[string]any {
		return map[string]any{"interface": map[string]any{name: body}}
	}
	rows := []struct {
		name        string
		payload     any
		e, phantoms int
		hooks       int    // создание — 3 хука (ctrl ×2 + ifcreated), снос — 1
		has         string // имя, которое обязано быть после вызова ("" — не проверять)
		gone        string // имя, которого быть не должно
		wantInResp  string
	}{
		{"payloads создаёт фантом", map[string]any{"interface": map[string]any{"name": "Wireguard5", "up": true}}, 0, 1, 3, "Wireguard5", "", "{}"},
		{"payloads no отсутствующего", map[string]any{"interface": map[string]any{"name": "Wireguard5", "no": true}}, 1, 0, 0, "", "Wireguard5", `unable to find interface \"Wireguard5\"`},
		{"command no отсутствующего", iface("OpkgTun1", map[string]any{"no": true}), 1, 0, 0, "", "OpkgTun1", "unable to find interface"},
		{"payloads no присутствующего", map[string]any{"interface": map[string]any{"name": "Wireguard0", "no": true}}, 0, 0, 1, "", "Wireguard0", "{}"},
		{"parse interface up", map[string]any{"parse": "interface Proxy2 up"}, 0, 1, 3, "Proxy2", "", "{}"},
		{"parse no interface отсутствующего", map[string]any{"parse": "no interface Proxy2"}, 1, 0, 0, "", "Proxy2", "unable to find interface"},
		{"parse no interface присутствующего", map[string]any{"parse": "no interface Wireguard0"}, 0, 0, 1, "", "Wireguard0", "{}"},
		{"parse no interface X настройка присутствующего", map[string]any{"parse": "no interface Wireguard0 ip access-group A in"}, 0, 0, 0, "Wireguard0", "", "{}"},
		{"parse no interface X настройка отсутствующего", map[string]any{"parse": "no interface Wireguard6 ip access-group A in"}, 0, 1, 3, "Wireguard6", "", "{}"},
		{"parse access-list", map[string]any{"parse": "access-list _WEBADMIN_x permit ip any any"}, 0, 0, 0, "Wireguard0", "", "{}"},
		{"system-name отсутствующего", map[string]any{"show": map[string]any{"interface": map[string]any{"system-name": map[string]any{"name": "Wireguard7"}}}}, 1, 0, 0, "", "Wireguard7", "6553619"},
		{"system-name присутствующего", map[string]any{"show": map[string]any{"interface": map[string]any{"system-name": map[string]any{"name": "Wireguard0"}}}}, 0, 0, 0, "Wireguard0", "", `"system-name":"nwg0"`},
		{"to-interface на отсутствующий", map[string]any{"ip": map[string]any{"static": map[string]any{"interface": "Wireguard0", "to-interface": "OpkgTun9"}}}, 1, 0, 0, "", "OpkgTun9", "no such interface: OpkgTun9."},
		{"ссылка на присутствующий", map[string]any{"ip": map[string]any{"route": map[string]any{"default": true, "interface": "Wireguard0"}}}, 0, 0, 0, "", "", "{}"},
		{"пакет", []any{showByName("Wireguard7"), iface("Wireguard3", map[string]any{"up": false}),
			map[string]any{"ip": map[string]any{"nat": []any{map[string]any{"interface": "OpkgTun4"}}}}}, 2, 1, 3, "Wireguard3", "", `[{"show"`},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			f := NewFakeNDMS(ndms.Interface{ID: "Wireguard0", Type: "Wireguard", SystemName: "nwg0"})
			raw, err := f.Post(context.Background(), r.payload)
			if err != nil || !strings.Contains(string(raw), r.wantInResp) {
				t.Fatalf("err=%v raw=%s, want substring %s", err, raw, r.wantInResp)
			}
			if f.E != r.e || f.Phantoms != r.phantoms {
				t.Fatalf("E=%d Phantoms=%d, want %d/%d", f.E, f.Phantoms, r.e, r.phantoms)
			}
			if hooks := f.DrainHooks(); len(hooks) != r.hooks {
				t.Fatalf("hooks=%+v, want %d", hooks, r.hooks)
			}
			if (r.has != "" && !f.Has(r.has)) || (r.gone != "" && f.Has(r.gone)) {
				t.Fatalf("has(%s)=%v gone(%s)=%v", r.has, f.Has(r.has), r.gone, f.Has(r.gone))
			}
			if strings.HasPrefix(r.name, "system-name отсутствующего") && parseSystemName(raw) != "" {
				t.Fatalf("parseSystemName(%s) = %q, want \"\"", raw, parseSystemName(raw))
			}
		})
	}
}

// F569: живое устройство opkgtunN не даёт NDMS создать запись OpkgTunN —
// C 0xcffd00a9, а каждый прочий ключ того же тела пишет E по несозданной
// записи. Голое создание — только C; без устройства — создаётся.
func TestFakeNDMS_CreateOpkgTunOverDevice_CAndE(t *testing.T) {
	ctx := context.Background()
	f := NewFakeNDMS()
	f.ExpectCreate("OpkgTun3")
	f.SetNetdev("opkgtun3", true)

	raw, err := f.Post(ctx, map[string]any{"interface": map[string]any{"OpkgTun3": map[string]any{
		"description": "x", "security-level": map[string]any{"public": true}}}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "0xcffd00a9") {
		t.Fatalf("ответ без 0xcffd00a9: %s", raw)
	}
	if f.C != 1 || f.E != 2 || f.Has("OpkgTun3") || f.Phantoms != 0 {
		t.Fatalf("склеенное: C=%d E=%d Has=%v Phantoms=%d, want 1/2/false/0", f.C, f.E, f.Has("OpkgTun3"), f.Phantoms)
	}

	bare := map[string]any{"interface": map[string]any{"OpkgTun3": map[string]any{}}}
	if _, err := f.Post(ctx, bare); err != nil {
		t.Fatal(err)
	}
	if f.C != 2 || f.E != 2 || f.Has("OpkgTun3") {
		t.Fatalf("голое при устройстве: C=%d E=%d Has=%v, want 2/2/false", f.C, f.E, f.Has("OpkgTun3"))
	}

	f.SetNetdev("opkgtun3", false)
	if _, err := f.Post(ctx, bare); err != nil {
		t.Fatal(err)
	}
	if !f.Has("OpkgTun3") || len(f.Created) != 1 || f.Created[0] != "OpkgTun3" || f.C != 2 || f.E != 2 || f.Phantoms != 0 {
		t.Fatalf("без устройства: Has=%v Created=%v C=%d E=%d Phantoms=%d", f.Has("OpkgTun3"), f.Created, f.C, f.E, f.Phantoms)
	}
}

// Поля сверх toWire (SetDetail) отдаются одинаково в списке и в точечном
// ответе — как у NDMS, где ключи записи идентичны (стенд 30.09).
func TestFakeNDMS_SetDetail_SameBytesInListAndPoint(t *testing.T) {
	ctx := context.Background()
	f := NewFakeNDMS(ndms.Interface{ID: "Wireguard0", Type: "Wireguard", SystemName: "nwg0"})
	f.SetDetail("Wireguard0", json.RawMessage(`{"wireguard":{"peer":[{"rxbytes":7}]}}`))

	rawList, err := f.GetRaw(ctx, "/show/interface/")
	if err != nil {
		t.Fatal(err)
	}
	var list map[string]map[string]json.RawMessage
	if err := json.Unmarshal(rawList, &list); err != nil {
		t.Fatal(err)
	}
	rawOne, err := f.Post(ctx, showByName("Wireguard0"))
	if err != nil {
		t.Fatal(err)
	}
	var one struct {
		Show struct {
			Interface map[string]json.RawMessage `json:"interface"`
		} `json:"show"`
	}
	if err := json.Unmarshal(rawOne, &one); err != nil {
		t.Fatal(err)
	}
	inList, inOne := string(list["Wireguard0"]["wireguard"]), string(one.Show.Interface["wireguard"])
	if inList == "" || inList != inOne || !strings.Contains(inList, `"rxbytes":7`) {
		t.Fatalf("wireguard: список %s, точечно %s", inList, inOne)
	}
	if string(list["Wireguard0"]["type"]) != `"Wireguard"` {
		t.Fatalf("поля toWire потеряны: %s", rawList)
	}
}

// Полное дерево rc — карта по присутствующим, без E; по имени отсутствующего — E.
func TestFakeNDMS_RCList_NoEForAbsentNames(t *testing.T) {
	ctx := context.Background()
	f := NewFakeNDMS(
		ndms.Interface{ID: "Wireguard0", Type: "Wireguard"},
		ndms.Interface{ID: "Wireguard1", Type: "Wireguard"})
	f.SetRC("Wireguard0", json.RawMessage(`{"wireguard":{"listen-port":51820}}`))
	f.SetRC("Wireguard9", json.RawMessage(`{"description":"снят"}`))

	raw, err := f.GetRaw(ctx, "/show/rc/interface/")
	if err != nil {
		t.Fatal(err)
	}
	var tree map[string]json.RawMessage
	if err := json.Unmarshal(raw, &tree); err != nil {
		t.Fatal(err)
	}
	if len(tree) != 2 || string(tree["Wireguard1"]) != `{}` || !strings.Contains(string(tree["Wireguard0"]), "51820") {
		t.Fatalf("дерево: %s", raw)
	}
	if f.RCListCalls() != 1 || f.E != 0 {
		t.Fatalf("RCListCalls=%d E=%d, want 1/0", f.RCListCalls(), f.E)
	}
	if _, err := f.GetRaw(ctx, "/show/rc/interface/Nope"); err == nil || f.E != 1 {
		t.Fatalf("по имени отсутствующего: err=%v E=%d", err, f.E)
	}
}

func TestFakeNDMS_RC_ByNameServesSetRC(t *testing.T) {
	ctx := context.Background()
	f := NewFakeNDMS(ndms.Interface{ID: "Wireguard0", Type: "Wireguard"})
	f.SetRC("Wireguard0", json.RawMessage(`{"description":"d","wireguard":{"asc":{"jc":4}}}`))

	raw, err := f.GetRaw(ctx, "/show/rc/interface/Wireguard0")
	if err != nil || !strings.Contains(string(raw), `"description":"d"`) {
		t.Fatalf("rc: err=%v raw=%s", err, raw)
	}
	raw, err = f.GetRaw(ctx, "/show/rc/interface/Wireguard0/wireguard/asc")
	if err != nil || string(raw) != `{"jc":4}` {
		t.Fatalf("asc: err=%v raw=%s", err, raw)
	}
	if f.E != 0 || f.RCListCalls() != 0 {
		t.Fatalf("E=%d RCListCalls=%d", f.E, f.RCListCalls())
	}
}

func TestFakeNDMS_FailRCList(t *testing.T) {
	ctx := context.Background()
	f := NewFakeNDMS(ndms.Interface{ID: "Wireguard0", Type: "Wireguard"})
	f.FailRCList(errors.New("rci down"))
	if _, err := f.GetRaw(ctx, "/show/rc/interface/"); err == nil || f.RCListCalls() != 1 {
		t.Fatalf("err=%v RCListCalls=%d", err, f.RCListCalls())
	}
	f.FailRCList(nil)
	if _, err := f.GetRaw(ctx, "/show/rc/interface/"); err != nil || f.RCListCalls() != 2 {
		t.Fatalf("после снятия: err=%v RCListCalls=%d", err, f.RCListCalls())
	}
}

// showByName — точечный `show interface name=X` (в проде его нет, F546):
// оракул обязан его моделировать и считать E на отсутствующем.
func showByName(name string) any {
	return map[string]any{"show": map[string]any{"interface": map[string]any{"name": name}}}
}
