package router

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// mtuFailOpkg — оракул, у которого падает SetMTU: сбой ПОСЛЕ provision,
// адреса и ACL — откат обязан разобрать уже настроенную запись.
type mtuFailOpkg struct{ *oracleOpkg }

func (m mtuFailOpkg) SetMTU(context.Context, string, int) error {
	return errors.New("injected: SetMTU")
}

func hasDeletePost(posts []string, name string) bool {
	for _, p := range posts {
		if strings.Contains(p, `{"`+name+`":{"no":true}}`) {
			return true
		}
	}
	return false
}

// M1: включение переиспользовало удержанную запись (R45) и упало дальше —
// откат её удерживает (down + снятие адресов), а не сносит: запись и permit
// в политике живы, `no interface` не уходит.
func TestM1_PolicyTunRollbackHoldsReusedRecord(t *testing.T) {
	h := newPolicyTunEnableHarness(t, "")
	pol := h.withPolicy(t, "Policy0")
	o := wireOracle(t, h)
	o.f.ExpectCreate("OpkgTun0")
	ctx := context.Background()

	if err := h.svc.Enable(ctx); err != nil {
		t.Fatalf("Enable #1: %v", err)
	}
	if err := h.svc.Disable(ctx); err != nil {
		t.Fatalf("Disable: %v", err)
	}
	if len(pol.permits) == 0 {
		t.Fatalf("permit не поставлен первым включением")
	}
	h.svc.deps.OpkgTun = mtuFailOpkg{o}
	mark := len(o.f.Posts)

	if err := h.svc.Enable(ctx); err == nil {
		t.Fatal("Enable #2 обязан упасть на SetMTU")
	}
	if !o.f.Has("OpkgTun0") {
		t.Fatal("откат снёс переиспользованную запись")
	}
	if hasDeletePost(o.f.Posts[mark:], "OpkgTun0") {
		t.Fatalf("откат послал no interface: %v", o.f.Posts[mark:])
	}
	if len(pol.denies) != 0 {
		t.Fatalf("откат снял permit: %v", pol.denies)
	}
	cleared := false
	for _, p := range o.f.Posts[mark:] {
		cleared = cleared || (strings.Contains(p, `"OpkgTun0"`) && strings.Contains(p, `"address"`) && strings.Contains(p, `"no":true`))
	}
	if !cleared {
		t.Fatalf("удержание обязано снять адрес: %v", o.f.Posts[mark:])
	}
	if st := h.loadPolicyTun(t); st == nil || st.Provisioned || st.Index != 0 {
		t.Fatalf("persist = %+v, want удержанный 0 без Provisioned", st)
	}
	o.clean(t)
}

// Контрапункт: запись создана ЭТИМ включением — откат её сносит, как раньше.
func TestM1_PolicyTunRollbackTearsDownCreatedRecord(t *testing.T) {
	h := newPolicyTunEnableHarness(t, "")
	o := wireOracle(t, h)
	o.f.ExpectCreate("OpkgTun0")
	h.svc.deps.OpkgTun = mtuFailOpkg{o}

	if err := h.svc.Enable(context.Background()); err == nil {
		t.Fatal("Enable обязан упасть на SetMTU")
	}
	if len(o.f.Created) != 1 {
		t.Fatalf("создания = %v, want 1", o.f.Created)
	}
	if o.f.Has("OpkgTun0") {
		t.Fatal("созданная этим включением запись обязана быть снесена")
	}
	if !hasDeletePost(o.f.Posts, "OpkgTun0") {
		t.Fatalf("нет no interface: %v", o.f.Posts)
	}
	o.clean(t)
}

// fakeip делит развилку: запись с нашим description переиспользована →
// откат удерживает (Delete нет), создана → сносит.
func TestM1_FakeIPRollbackHoldVsTeardown(t *testing.T) {
	for _, tc := range []struct {
		name   string
		reused bool
	}{{"reused", true}, {"created", false}} {
		t.Run(tc.name, func(t *testing.T) {
			h := newFakeIPEnableHarness(t, "SetMTU")
			if tc.reused {
				h.opkg.records = map[string]string{"OpkgTun0": fakeIPTunDescription}
			}
			old := fakeIPLinkPresent
			fakeIPLinkPresent = func(context.Context, string) bool { return tc.reused }
			t.Cleanup(func() { fakeIPLinkPresent = old })
			if err := h.svc.Enable(context.Background()); err == nil {
				t.Fatal("Enable обязан упасть на SetMTU")
			}
			deleted := h.log.idxOf("Delete:OpkgTun0") >= 0
			created := h.log.idxOf("Create:OpkgTun0:private") >= 0
			if deleted == tc.reused || created == tc.reused {
				t.Fatalf("reused=%v: Create=%v Delete=%v, calls %v", tc.reused, created, deleted, h.log.calls)
			}
			if tc.reused && h.log.idxOf("SetSecurityLevel:OpkgTun0:private") < 0 {
				t.Fatalf("удержанная OpkgTun0 не переиспользована: %v", h.log.calls)
			}
			if tc.reused && h.log.idxOf("InterfaceDown:OpkgTun0") < 0 {
				t.Fatalf("удержание без down: %v", h.log.calls)
			}
		})
	}
}
