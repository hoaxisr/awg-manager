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
