package query

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/ndms/transport"
)

func TestFakeNDMS_PointReadAbsentCountsE(t *testing.T) {
	f := NewFakeNDMS(ndms.Interface{ID: "Wireguard0", Type: "Wireguard", SystemName: "nwg0"})
	raw, err := f.Post(context.Background(), transport.ShowInterface("Wireguard7", nil))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"6553619"`) || f.E != 1 {
		t.Fatalf("want unable-to-find envelope and E=1, got %s E=%d", raw, f.E)
	}
	if _, err := f.Post(context.Background(), transport.ShowInterface("Wireguard0", nil)); err != nil || f.E != 1 {
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
	if len(hooks) != 1 || hooks[0] != (FakeHook{Type: "ifcreated", ID: "Wireguard2"}) {
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

// Наша форма создания считается фантомом, но попадает в Created; внешний
// Remove кладёт ifdestroyed; список читается InterfaceStore, считается и
// отказывает по FailList.
func TestFakeNDMS_CreatedRemoveAndList(t *testing.T) {
	ctx := context.Background()
	f := NewFakeNDMS(ndms.Interface{ID: "Wireguard0", Type: "Wireguard", SystemName: "nwg0"})
	_, _ = f.Post(ctx, map[string]any{"interface": map[string]any{"OpkgTun3": map[string]any{
		"description": "d", "security-level": map[string]any{"public": true}}}})
	if f.Phantoms != 1 || len(f.Created) != 1 || f.Created[0] != "OpkgTun3" {
		t.Fatalf("create: Phantoms=%d Created=%v", f.Phantoms, f.Created)
	}
	f.DrainHooks()
	f.Remove("Wireguard0")
	if hooks := f.DrainHooks(); len(hooks) != 1 || hooks[0] != (FakeHook{Type: "ifdestroyed", ID: "Wireguard0"}) {
		t.Fatalf("hooks: %+v", hooks)
	}

	s := NewInterfaceStore(f, NopLogger())
	list, err := s.List(ctx)
	if err != nil || len(list) != 1 || list[0].ID != "OpkgTun3" || list[0].Type != "OpkgTun" || f.ListCalls() != 1 {
		t.Fatalf("list=%+v err=%v calls=%d", list, err, f.ListCalls())
	}
	f.FailList(errors.New("rci down"))
	if _, err := f.Post(ctx, map[string]any{"show": map[string]any{"interface": map[string]any{}}}); err == nil || f.ListCalls() != 2 {
		t.Fatalf("FailList: err=%v calls=%d", err, f.ListCalls())
	}
}
