package subscription

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

const (
	inlineSeedURL   = "vless://3a3b1c2e-9999-4321-aaaa-1234567890ab@h.example:443?security=tls&sni=a#A\n"
	inlineManualURL = "vless://3a3b1c2e-9999-4321-aaaa-1234567890ac@m.example:443?security=tls&sni=b#M"
)

func withFakeIface(t *testing.T, names ...string) {
	t.Helper()
	tempNet := t.TempDir()
	for _, n := range names {
		_ = os.MkdirAll(filepath.Join(tempNet, n), 0755)
	}
	oldRoot := sysClassNet
	sysClassNet = tempNet
	t.Cleanup(func() { sysClassNet = oldRoot })
}

// memberBinds — bind_interface по тегам состава подписки.
func memberBinds(mut *fakeMutator, sub *Subscription) map[string]any {
	got := map[string]any{}
	for _, ob := range mut.SubscriptionOutbounds() {
		tag, _ := ob["tag"].(string)
		if slices.Contains(sub.MemberTags, tag) {
			got[tag] = ob["bind_interface"]
		}
	}
	return got
}

// Смена интерфейса не должна разбирать inline заново: добавленный вручную
// сервер живёт только в Members, из исходного текста он не восстановится (F769).
func TestService_InlineBindChange_KeepsManualMember(t *testing.T) {
	withFakeIface(t, "eth3")
	svc, mut := newTestService(t)
	ctx := context.Background()
	sub, err := svc.Create(ctx, CreateInput{Label: "i", Inline: inlineSeedURL, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if sub, err = svc.AddManualMember(ctx, sub.ID, inlineManualURL); err != nil {
		t.Fatal(err)
	}

	bind := "eth3"
	newMode := ModeURLTest
	urltest := URLTestConfig{IntervalSec: 120, ToleranceMs: 100}
	for _, step := range []struct {
		name     string
		patch    UpdatePatch
		wantBind any
	}{
		{"bind", UpdatePatch{BindInterface: &bind}, "eth3"},
		{"unbind", UpdatePatch{BindInterface: new(string)}, nil},
		{"with mode", UpdatePatch{BindInterface: &bind, Mode: &newMode, URLTest: &urltest}, "eth3"},
	} {
		sub, err = svc.Update(sub.ID, step.patch)
		if err != nil {
			t.Fatalf("%s: %v", step.name, err)
		}
		if len(sub.MemberTags) != 2 || len(sub.Members) != 2 || len(sub.OrphanTags) != 0 {
			t.Fatalf("%s: members=%d tags=%v orphans=%v, manual server lost", step.name, len(sub.Members), sub.MemberTags, sub.OrphanTags)
		}
		binds := memberBinds(mut, sub)
		if len(binds) != 2 {
			t.Fatalf("%s: bodies=%v, want 2", step.name, binds)
		}
		for tag, got := range binds {
			if got != step.wantBind {
				t.Errorf("%s: %s bind_interface=%v, want %v", step.name, tag, got, step.wantBind)
			}
		}
	}

	if sub.Mode != ModeURLTest {
		t.Fatalf("mode=%q, want urltest", sub.Mode)
	}
	if got := selectorMembers(t, mut, sub.SelectorTag); len(got) != 2 {
		t.Fatalf("group members=%v, want 2", got)
	}
	for _, ob := range mut.SubscriptionOutbounds() {
		if ob["tag"] == sub.SelectorTag && ob["type"] != "urltest" {
			t.Fatalf("group type=%v, want urltest", ob["type"])
		}
	}
}

// Сервер, снятый вручную, не возвращается из исходного текста.
func TestService_InlineBindChange_KeepsRemovedMemberRemoved(t *testing.T) {
	withFakeIface(t, "eth3")
	svc, _ := newTestService(t)
	ctx := context.Background()
	sub, err := svc.Create(ctx, CreateInput{Label: "i", Inline: inlineSeedURL, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	seedTag := sub.MemberTags[0]
	if sub, err = svc.AddManualMember(ctx, sub.ID, inlineManualURL); err != nil {
		t.Fatal(err)
	}
	if sub, err = svc.RemoveMember(ctx, sub.ID, seedTag); err != nil {
		t.Fatal(err)
	}
	bind := "eth3"
	if sub, err = svc.Update(sub.ID, UpdatePatch{BindInterface: &bind}); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(sub.MemberTags, seedTag) || len(sub.MemberTags) != 1 {
		t.Fatalf("tags=%v, removed server is back", sub.MemberTags)
	}
}

// Снятие MLKEM вместе со сменой интерфейса идёт тем же путём и снимает флаг.
func TestService_InlineBindChange_WithMLKEMOff(t *testing.T) {
	withFakeIface(t, "eth3")
	svc, mut := newTestService(t)
	ctx := context.Background()
	sub, err := svc.Create(ctx, CreateInput{Label: "i", Inline: mlkemRealityURL + "#A\n", Enabled: true, RealityMLKEM: true})
	if err != nil {
		t.Fatal(err)
	}
	if sub, err = svc.AddManualMember(ctx, sub.ID, inlineManualURL); err != nil {
		t.Fatal(err)
	}
	bind, off := "eth3", false
	if sub, err = svc.Update(sub.ID, UpdatePatch{BindInterface: &bind, RealityMLKEM: &off}); err != nil {
		t.Fatal(err)
	}
	if len(sub.MemberTags) != 2 {
		t.Fatalf("tags=%v, manual server lost", sub.MemberTags)
	}
	if got := mlkemStates(t, mut, sub)["h.example"]; got != nil {
		t.Fatalf("flag=%v, want cleared", got)
	}
}

// Сирота, оставленный в слоте ссылкой другого слота (F761), получает новый bind.
func TestService_InlineBindChange_UpdatesSlotOrphan(t *testing.T) {
	withFakeIface(t, "eth3")
	svc, mut := newTestService(t)
	mut.slot = map[string]bool{}
	ctx := context.Background()
	sub, err := svc.Create(ctx, CreateInput{Label: "i", Inline: inlineSeedURL, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	orphan := sub.SelectorTag + "-orphan"
	if err := mut.AddOutbound(orphan, []byte(`{"type":"vless","server":"o.example"}`)); err != nil {
		t.Fatal(err)
	}
	foreign := "other-slot"
	if err := mut.AddOutbound(foreign, []byte(`{"type":"vless","server":"f.example"}`)); err != nil {
		t.Fatal(err)
	}
	bind := "eth3"
	if _, err := svc.Update(sub.ID, UpdatePatch{BindInterface: &bind}); err != nil {
		t.Fatal(err)
	}
	for _, ob := range mut.SubscriptionOutbounds() {
		switch ob["tag"] {
		case orphan:
			if ob["bind_interface"] != "eth3" {
				t.Errorf("orphan bind_interface=%v, want eth3", ob["bind_interface"])
			}
		case foreign:
			if _, ok := ob["bind_interface"]; ok {
				t.Errorf("foreign outbound touched: %v", ob)
			}
		}
	}
}

// Сбой staging откатывает и батч, и настройки: иначе полупереписанные тела
// закоммитил бы следующий Reload.
func TestService_InlineBindChange_RollsBackBatchOnStageFailure(t *testing.T) {
	withFakeIface(t, "eth3")
	svc, mut := newTestService(t)
	sub, err := svc.Create(context.Background(), CreateInput{Label: "i", Inline: inlineSeedURL, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	mut.updateErr = errors.New("boom")
	before := mut.rollbacks
	bind := "eth3"
	if _, err := svc.Update(sub.ID, UpdatePatch{BindInterface: &bind}); err == nil {
		t.Fatal("expected error")
	}
	if mut.rollbacks != before+1 {
		t.Fatalf("rollbacks=%d, want %d", mut.rollbacks, before+1)
	}
	got, err := svc.store.Get(sub.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.BindInterface != "" {
		t.Fatalf("bind=%q, want rolled back", got.BindInterface)
	}
}
