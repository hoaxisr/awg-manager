package query

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
)

// Фикстура — форма running-config стенда 5.01 (2026-09-05): тело блока с
// отступом в 4 пробела, две привязки в порядке появления, форма `no …` рядом,
// чужой блок с той же строкой.
func newRCStore(t *testing.T, lines ...string) *RunningConfigStore {
	t.Helper()
	fg := NewFakeGetter()
	b, _ := json.Marshal(map[string]any{"message": lines})
	fg.SetJSON("/show/running-config", string(b))
	return NewRunningConfigStore(fg, NopLogger())
}

func TestInterfaceAccessGroups_OrderNoFormAndForeignBlock(t *testing.T) {
	s := newRCStore(t,
		"interface OpkgTun10",
		"    description awgm-acl-probe",
		"    security-level private",
		"    ip address 10.66.0.1 255.255.0.0",
		"    ip access-group AWGMTEST in",
		"    ip access-group _WEBADMIN_OpkgTun10 in",
		"    no ip access-group GHOST in",
		"    up",
		"!",
		"interface Wireguard0",
		"    ip access-group OTHER in",
		"!",
	)
	got, err := s.InterfaceAccessGroups(context.Background(), "OpkgTun10")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"AWGMTEST", "_WEBADMIN_OpkgTun10"}; !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	if got, _ := s.InterfaceAccessGroups(context.Background(), "Wireguard0"); !slices.Equal(got, []string{"OTHER"}) {
		t.Fatalf("чужой блок: %v", got)
	}
	if got, _ := s.InterfaceAccessGroups(context.Background(), "OpkgTun99"); len(got) != 0 || got == nil {
		t.Fatalf("нет блока → пустой НЕ-nil срез, got %#v", got)
	}
}

// Ошибка чтения running-config всплывает, а не маскируется пустым списком.
func TestInterfaceAccessGroups_PropagatesFetchError(t *testing.T) {
	s := NewRunningConfigStore(NewFakeGetter(), NopLogger()) // без SetJSON → errNoFakeResponse
	if _, err := s.InterfaceAccessGroups(context.Background(), "OpkgTun10"); err == nil {
		t.Fatal("ожидалась ошибка")
	}
}

// ACLRulesOf разбирает блок списка: правила отдаёт, флаг `auto-delete` — нет
// (снять его отдельной командой нельзя, и «список чужой» он не означает).
// Заголовок передаётся целиком, потому что у IPv6 своё пространство списков с
// тем же именем — `ipv6 access-list _WEBADMIN_X` и `access-list _WEBADMIN_X`
// это разные блоки.
func TestACLRulesOf_BlockAndFlags(t *testing.T) {
	lines := []string{
		"access-list _WEBADMIN_Wireguard0",
		"    permit tcp 10.77.0.2 255.255.255.255 0.0.0.0 0.0.0.0",
		"    permit ip 0.0.0.0 0.0.0.0 0.0.0.0 0.0.0.0",
		"    auto-delete",
		"! ",
		"ipv6 access-list _WEBADMIN_Wireguard0",
		"    permit ipv6 ::/0 ::/0",
		"! ",
	}
	if got, want := ACLRulesOf(lines, "access-list _WEBADMIN_Wireguard0"), []string{
		"permit tcp 10.77.0.2 255.255.255.255 0.0.0.0 0.0.0.0",
		"permit ip 0.0.0.0 0.0.0.0 0.0.0.0 0.0.0.0",
	}; !slices.Equal(got, want) {
		t.Fatalf("v4: got %v want %v", got, want)
	}
	if got, want := ACLRulesOf(lines, "ipv6 access-list _WEBADMIN_Wireguard0"),
		[]string{"permit ipv6 ::/0 ::/0"}; !slices.Equal(got, want) {
		t.Fatalf("v6: got %v want %v", got, want)
	}
	if got := ACLRulesOf(lines, "access-list _WEBADMIN_Wireguard9"); len(got) != 0 || got == nil {
		t.Fatalf("незнакомый список: got %v, ждали пустой не-nil", got)
	}
}
