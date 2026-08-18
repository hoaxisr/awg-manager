package router

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

func TestPatchOutboundBindInterface(t *testing.T) {
	raw := json.RawMessage(`{"type":"socks","tag":"t1","server":"1.2.3.4","server_port":1080}`)
	withBind, err := patchOutboundBindInterface(raw, "eth3")
	if err != nil {
		t.Fatal(err)
	}
	var ob map[string]any
	if err := json.Unmarshal(withBind, &ob); err != nil {
		t.Fatal(err)
	}
	if ob["bind_interface"] != "eth3" {
		t.Fatalf("bind_interface = %v", ob["bind_interface"])
	}
	cleared, err := patchOutboundBindInterface(withBind, "")
	if err != nil {
		t.Fatal(err)
	}
	var clearedOb map[string]any
	if err := json.Unmarshal(cleared, &clearedOb); err != nil {
		t.Fatal(err)
	}
	if _, ok := clearedOb["bind_interface"]; ok {
		t.Fatalf("expected bind_interface removed")
	}
}

type fakeTunnelEditor struct {
	tags    map[string]json.RawMessage
	updated map[string]json.RawMessage
}

func (f *fakeTunnelEditor) GetTunnelOutbound(_ context.Context, tag string) (json.RawMessage, error) {
	raw, ok := f.tags[tag]
	if !ok {
		return nil, ErrOutboundNotFound
	}
	return raw, nil
}

func (f *fakeTunnelEditor) UpdateTunnelOutbounds(_ context.Context, updates map[string]json.RawMessage) error {
	for tag, out := range updates {
		f.updated[tag] = out
		f.tags[tag] = out
	}
	return nil
}

func (f *fakeTunnelEditor) IsSingboxTunnelTag(_ context.Context, tag string) bool {
	_, ok := f.tags[tag]
	return ok
}

func TestApplyCompositeEgressBind(t *testing.T) {
	editor := &fakeTunnelEditor{
		tags: map[string]json.RawMessage{
			"t1": json.RawMessage(`{"type":"socks","tag":"t1","server":"1.2.3.4","bind_interface":"eth1"}`),
			"t2": json.RawMessage(`{"type":"socks","tag":"t2","server":"2.2.2.2","bind_interface":"eth0"}`),
			"t3": json.RawMessage(`{"type":"socks","tag":"t3","server":"3.3.3.3"}`),
			"t4": json.RawMessage(`{"type":"socks","tag":"t4","server":"4.4.4.4","bind_interface":"eth9"}`),
		},
		updated: make(map[string]json.RawMessage),
	}

	settingsStore := storage.NewSettingsStore(t.TempDir())
	// Pre-seed composite egress binds: group1 previously had eth1
	st, _ := settingsStore.Load()
	st.SingboxRouter.CompositeEgressBinds = map[string]string{
		"group1": "eth1",
	}
	_ = settingsStore.Save(st)

	svc := &ServiceImpl{
		deps: Deps{
			SingboxTunnelsEditor: editor,
			Settings:             settingsStore,
		},
	}

	// Case 1: Update group1 to eth1 with members t2, t3 (t1 was removed, had eth1 from group1).
	err := svc.applyCompositeEgressBind(context.Background(), []string{"t1", "t2"}, []string{"t2", "t3"}, "group1", "eth1")
	if err != nil {
		t.Fatal(err)
	}

	// Verify t1 (removed) had bind cleared because it matched oldBind (eth1)
	if out1, ok := editor.updated["t1"]; !ok {
		t.Errorf("t1 should be updated (bind cleared)")
	} else {
		var ob map[string]any
		_ = json.Unmarshal(out1, &ob)
		if _, hasBind := ob["bind_interface"]; hasBind {
			t.Errorf("t1 bind_interface should be cleared")
		}
	}

	// Verify t2 (kept) had bind updated to eth1
	if out2, ok := editor.updated["t2"]; !ok {
		t.Errorf("t2 should be updated")
	} else {
		var ob map[string]any
		_ = json.Unmarshal(out2, &ob)
		if ob["bind_interface"] != "eth1" {
			t.Errorf("t2 bind_interface = %v, want eth1", ob["bind_interface"])
		}
	}

	// Verify t3 (added) had bind updated to eth1
	if out3, ok := editor.updated["t3"]; !ok {
		t.Errorf("t3 should be updated")
	} else {
		var ob map[string]any
		_ = json.Unmarshal(out3, &ob)
		if ob["bind_interface"] != "eth1" {
			t.Errorf("t3 bind_interface = %v, want eth1", ob["bind_interface"])
		}
	}

	// Case 2: Group clearing bind does not clear manual bind on t4 (eth9 != eth1)
	editor.updated = make(map[string]json.RawMessage)
	err = svc.applyCompositeEgressBind(context.Background(), []string{"t2", "t3", "t4"}, []string{"t2", "t3", "t4"}, "group1", "")
	if err != nil {
		t.Fatal(err)
	}

	// t2 and t3 had eth1 (matching oldBind), so their bind is cleared
	if out2, ok := editor.updated["t2"]; !ok {
		t.Errorf("t2 should have bind cleared")
	} else {
		var ob map[string]any
		_ = json.Unmarshal(out2, &ob)
		if _, hasBind := ob["bind_interface"]; hasBind {
			t.Errorf("t2 bind_interface should be cleared")
		}
	}

	// t4 had manual eth9, so it should NOT be touched
	if _, ok := editor.updated["t4"]; ok {
		t.Errorf("t4 has manual bind eth9, should not be modified when group clears bind")
	}
}
