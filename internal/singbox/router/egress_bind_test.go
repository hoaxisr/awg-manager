package router

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// withKernelIfaces temporarily overrides the package-level
// kernelInterfaceExists for the duration of fn. Tests use this to
// avoid asserting on a real /sys/class/net state — bind names like
// "eth1" are not real interfaces on the test host.
func withKernelIfaces(t *testing.T, present map[string]bool, fn func()) {
	t.Helper()
	prev := kernelInterfaceExists
	kernelInterfaceExists = func(name string) bool {
		return present[name]
	}
	defer func() { kernelInterfaceExists = prev }()
	fn()
}

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
	staged  map[string]json.RawMessage
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

func (f *fakeTunnelEditor) StageTunnelOutboundUpdates(_ context.Context, updates map[string]json.RawMessage) error {
	for tag, out := range updates {
		f.staged[tag] = out
		// In a real deploy, the orchestrator's debounced ApplyDraft
		// merges pending/ into active/ and reloads. The test harness
		// is single-shot, so we mirror the post-apply state into
		// tags so the next call sees the patched bind as the
		// current one.
		f.tags[tag] = out
	}
	return nil
}

func (f *fakeTunnelEditor) IsSingboxTunnelTag(_ context.Context, tag string) (bool, error) {
	_, ok := f.tags[tag]
	return ok, nil
}

func TestApplyCompositeEgressBind(t *testing.T) {
	withKernelIfaces(t, map[string]bool{
		"eth0": true, "eth1": true, "eth3": true, "eth9": true,
	}, func() {
		editor := &fakeTunnelEditor{
			tags: map[string]json.RawMessage{
				// t1 was a member of group1 (oldBind=eth1) and is being removed.
				"t1": json.RawMessage(`{"type":"socks","tag":"t1","server":"1.2.3.4","bind_interface":"eth1"}`),
				// t2 is a member of group1 carrying group1's bind.
				"t2": json.RawMessage(`{"type":"socks","tag":"t2","server":"2.2.2.2","bind_interface":"eth1"}`),
				// t3 is the new member; it has no bind yet.
				"t3": json.RawMessage(`{"type":"socks","tag":"t3","server":"3.3.3.3"}`),
				// t4 has a manual bind unrelated to any group — must be left alone.
				"t4": json.RawMessage(`{"type":"socks","tag":"t4","server":"4.4.4.4","bind_interface":"eth9"}`),
			},
			updated: make(map[string]json.RawMessage),
			staged:  make(map[string]json.RawMessage),
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

		// Case 1: Update group1 to eth1 with members t2, t3 (t1 was removed).
		err := svc.applyCompositeEgressBind(context.Background(), []string{"t1", "t2"}, []string{"t2", "t3"}, "group1", "eth1")
		if err != nil {
			t.Fatal(err)
		}

		// Patches must land in the staging buffer (not the immediate
		// update buffer) so they ride the orchestrator draft (#709,
		// PR #732 review blocker #2).
		if _, ok := editor.updated["t1"]; ok {
			t.Errorf("t1 should NOT be in immediate updated (write+SIGHUP) — bind changes must stage")
		}
		if _, ok := editor.updated["t2"]; ok {
			t.Errorf("t2 should NOT be in immediate updated — bind changes must stage")
		}
		if _, ok := editor.updated["t3"]; ok {
			t.Errorf("t3 should NOT be in immediate updated — bind changes must stage")
		}

		// Verify t1 (removed) had bind cleared because it matched oldBind (eth1)
		if out1, ok := editor.staged["t1"]; !ok {
			t.Errorf("t1 should be staged (bind cleared)")
		} else {
			var ob map[string]any
			_ = json.Unmarshal(out1, &ob)
			if _, hasBind := ob["bind_interface"]; hasBind {
				t.Errorf("t1 bind_interface should be cleared")
			}
		}

		// t2 already has the desired bind (eth1) so no patch is needed.
		if _, ok := editor.staged["t2"]; ok {
			t.Errorf("t2 already carries eth1 — no patch expected")
		}

		// Verify t3 (added) had bind set to eth1
		if out3, ok := editor.staged["t3"]; !ok {
			t.Errorf("t3 should be staged")
		} else {
			var ob map[string]any
			_ = json.Unmarshal(out3, &ob)
			if ob["bind_interface"] != "eth1" {
				t.Errorf("t3 bind_interface = %v, want eth1", ob["bind_interface"])
			}
		}

		// Case 2: Group clearing bind does not clear manual bind on t4 (eth9 != eth1)
		editor.staged = make(map[string]json.RawMessage)
		err = svc.applyCompositeEgressBind(context.Background(), []string{"t2", "t3", "t4"}, []string{"t2", "t3", "t4"}, "group1", "")
		if err != nil {
			t.Fatal(err)
		}

		// t2 carries eth1 (matching oldBind), so its bind is cleared.
		if out2, ok := editor.staged["t2"]; !ok {
			t.Errorf("t2 should have bind cleared")
		} else {
			var ob map[string]any
			_ = json.Unmarshal(out2, &ob)
			if _, hasBind := ob["bind_interface"]; hasBind {
				t.Errorf("t2 bind_interface should be cleared")
			}
		}

		// t4 has a manual bind eth9 — must not be modified when group clears bind.
		if _, ok := editor.staged["t4"]; ok {
			t.Errorf("t4 has manual bind eth9, should not be modified when group clears bind")
		}
	})
}

// TestApplyCompositeEgressBind_ForeignBindIsRespected ensures we never
// overwrite a bind_interface placed by another group (or by the user
// on the tunnel page). bind_interface has two owners in this codebase
// and the review explicitly flagged the previous behaviour as a "double
// owner of one field" leak.
func TestApplyCompositeEgressBind_ForeignBindIsRespected(t *testing.T) {
	withKernelIfaces(t, map[string]bool{
		"eth1": true, "eth9": true,
	}, func() {
		editor := &fakeTunnelEditor{
			tags: map[string]json.RawMessage{
				// t1 has a manual bind eth9 placed by the tunnel page.
				"t1": json.RawMessage(`{"type":"socks","tag":"t1","server":"1.2.3.4","bind_interface":"eth9"}`),
				"t2": json.RawMessage(`{"type":"socks","tag":"t2","server":"2.2.2.2"}`),
			},
			updated: make(map[string]json.RawMessage),
			staged:  make(map[string]json.RawMessage),
		}

		settingsStore := storage.NewSettingsStore(t.TempDir())
		svc := &ServiceImpl{
			deps: Deps{
				SingboxTunnelsEditor: editor,
				Settings:             settingsStore,
			},
		}

		// Add a new group with bind=eth1, members=[t1, t2]. t1's
		// foreign bind (eth9) must survive; t2 gets eth1.
		err := svc.applyCompositeEgressBind(context.Background(), nil, []string{"t1", "t2"}, "group1", "eth1")
		if err != nil {
			t.Fatal(err)
		}

		if _, ok := editor.staged["t1"]; ok {
			t.Errorf("t1 carries a foreign bind (eth9) — must not be patched")
		}
		if out2, ok := editor.staged["t2"]; !ok {
			t.Errorf("t2 should be staged with eth1")
		} else {
			var ob map[string]any
			_ = json.Unmarshal(out2, &ob)
			if ob["bind_interface"] != "eth1" {
				t.Errorf("t2 bind_interface = %v, want eth1", ob["bind_interface"])
			}
		}
	})
}

// TestApplyCompositeEgressBind_MissingInterfaceStripsBind verifies the
// self-heal path: when the requested bind points at a kernel interface
// that is not currently present, we must not land bind_interface on any
// member and we must clear the group's stored bind so the next reload
// does not FATAL-loop (#709, PR #732 review blocker #5).
func TestApplyCompositeEgressBind_MissingInterfaceStripsBind(t *testing.T) {
	withKernelIfaces(t, map[string]bool{
		// eth_usb0 is intentionally absent — simulates a USB modem
		// that was unplugged.
	}, func() {
		editor := &fakeTunnelEditor{
			tags: map[string]json.RawMessage{
				// t1 already has the doomed bind persisted from a
				// previous boot.
				"t1": json.RawMessage(`{"type":"socks","tag":"t1","server":"1.2.3.4","bind_interface":"eth_usb0"}`),
			},
			updated: make(map[string]json.RawMessage),
			staged:  make(map[string]json.RawMessage),
		}

		settingsStore := storage.NewSettingsStore(t.TempDir())
		svc := &ServiceImpl{
			deps: Deps{
				SingboxTunnelsEditor: editor,
				Settings:             settingsStore,
			},
		}

		err := svc.applyCompositeEgressBind(context.Background(), nil, []string{"t1"}, "group1", "eth_usb0")
		if err != nil {
			t.Fatal(err)
		}

		// t1 must have bind_interface removed, not re-set to eth_usb0.
		if out1, ok := editor.staged["t1"]; !ok {
			t.Errorf("t1 should be staged with bind stripped")
		} else {
			var ob map[string]any
			_ = json.Unmarshal(out1, &ob)
			if _, hasBind := ob["bind_interface"]; hasBind {
				t.Errorf("t1 bind_interface should be stripped (kernel iface missing)")
			}
		}
		// settings must record the bind as empty so the user can
		// re-apply when the interface returns.
		if stored, _ := settingsStore.Load(); stored.SingboxRouter.CompositeEgressBinds["group1"] != "" {
			t.Errorf("group1 stored bind should be empty, got %q",
				stored.SingboxRouter.CompositeEgressBinds["group1"])
		}
	})
}

// TestApplyCompositeEgressBind_GroupChangingToDirectStripsMembersBind
// pins the contract for the "selector/urltest → direct" edit. The
// direct branch of CompositeOutboundEditModal sends no egress_bind
// at all (the field is hidden), and the front-end gates the bind
// field behind `type === 'direct'`. The server therefore receives
// (tag, Outbound{Type:"direct"}, egressBind=nil), and must:
//   - clear bind_interface on every former member whose bind was
//     placed by this group (#709, PR #732 review blocker #4);
//   - drop the group's stored bind from settings so the slot does
//     not become a stale, UI-untouchable orphan.
//
// The direct group has no newMembers, so the patch cycle has to
// drive off oldMembers alone — exactly the path "Отменить черновик"
// relies on to keep the tunnels file clean.
func TestApplyCompositeEgressBind_GroupChangingToDirectStripsMembersBind(t *testing.T) {
	withKernelIfaces(t, map[string]bool{
		"eth0": true, "eth1": true, "eth9": true,
	}, func() {
		editor := &fakeTunnelEditor{
			tags: map[string]json.RawMessage{
				// t1, t2 carry the group1 bind (eth0) we are about
				// to discard.
				"t1": json.RawMessage(`{"type":"socks","tag":"t1","server":"1.2.3.4","bind_interface":"eth0"}`),
				"t2": json.RawMessage(`{"type":"socks","tag":"t2","server":"2.2.2.2","bind_interface":"eth0"}`),
				// t3 has a manual bind unrelated to this group —
				// must NOT be touched even though it could be
				// considered "left over" in the broader cleanup
				// sense.
				"t3": json.RawMessage(`{"type":"socks","tag":"t3","server":"3.3.3.3","bind_interface":"eth9"}`),
			},
			updated: make(map[string]json.RawMessage),
			staged:  make(map[string]json.RawMessage),
		}

		settingsStore := storage.NewSettingsStore(t.TempDir())
		st, _ := settingsStore.Load()
		st.SingboxRouter.CompositeEgressBinds = map[string]string{"group1": "eth0"}
		_ = settingsStore.Save(st)

		svc := &ServiceImpl{
			deps: Deps{
				SingboxTunnelsEditor: editor,
				Settings:             settingsStore,
			},
		}

		// Mimic the front-end direct branch: egressBind=nil, newMembers=nil.
		err := svc.applyCompositeEgressBind(context.Background(), []string{"t1", "t2", "t3"}, nil, "group1", "")
		if err != nil {
			t.Fatal(err)
		}

		// t1, t2: bind_interface stripped (was group-owned).
		for _, tag := range []string{"t1", "t2"} {
			out, ok := editor.staged[tag]
			if !ok {
				t.Errorf("%s should be staged with bind cleared", tag)
				continue
			}
			var ob map[string]any
			_ = json.Unmarshal(out, &ob)
			if _, has := ob["bind_interface"]; has {
				t.Errorf("%s bind_interface should be cleared", tag)
			}
		}
		// t3: foreign manual bind — left alone.
		if _, ok := editor.staged["t3"]; ok {
			t.Errorf("t3 carries a foreign bind (eth9) — must not be touched on direct edit")
		}

		// settings: group1 entry must be dropped (no orphan record).
		if stored, _ := settingsStore.Load(); stored.SingboxRouter.CompositeEgressBinds["group1"] != "" {
			t.Errorf("group1 stored bind should be cleared, got %q",
				stored.SingboxRouter.CompositeEgressBinds["group1"])
		}
	})
}
