package managed

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/logging"
)

// Running-config недоступен → сегменты всё равно применяются (4 прежние команды),
// а факт «permit-all не проверен» уходит в журнал приложения Warn.
func TestApplyLANSegments_RunningConfigUnavailable_WarnsAndProceeds(t *testing.T) {
	svc, store, poster := newLANSegmentsTestService(t) // stateAwareGetter: running-config = ошибка
	spy := &recAppLog{}
	svc.appLog = logging.NewScopedLogger(spy, logging.GroupServer, logging.SubManaged)
	seedServer(t, store, "Wireguard0")
	resetPosts(poster)
	if err := svc.SetLANSegments(context.Background(), "Wireguard0", []string{"Home"}); err != nil {
		t.Fatal(err)
	}
	if n := len(parseStrings(poster)); n != 4 {
		t.Fatalf("команд %d, ждали 4", n)
	}
	// SetLANSegments после применения пишет свой Info — две записи.
	if len(spy.entries) != 2 ||
		!strings.HasPrefix(spy.entries[0], "warn|lan-acl|Wireguard0|permit-all не проверен/не снят: ") ||
		spy.entries[1] != "info|lan-segments|Wireguard0|LAN segments changed: Home" {
		t.Fatalf("журнал = %v", spy.entries)
	}
}

// ForeignAccessGroups отдаёт чужие привязки в порядке появления и вычитает наш AWGM_.
func TestForeignAccessGroups_ExcludesOurs(t *testing.T) {
	svc, _, _ := newLANSegmentsTestService(t)
	withRunningConfig(svc, "interface Wireguard0", "    ip access-group AWGM_Wireguard0 in", "    ip access-group GUEST_ACL in", "    ip access-group _WEBADMIN_Wireguard0 in", "!")
	got, err := svc.ForeignAccessGroups(context.Background(), "Wireguard0")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"GUEST_ACL", "_WEBADMIN_Wireguard0"}; !slices.Equal(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

// Sweep снимает остаток ТОЛЬКО там, где он есть: два сервера, остаток у одного →
// ровно две команды и обе про него; чистый роутер → ноль RCI.
func TestSweepForeignPermitAll_OnlyWherePresent(t *testing.T) {
	svc, store, poster := newLANSegmentsTestService(t)
	seedServer(t, store, "Wireguard0")
	seedServer(t, store, "Wireguard1") // стор проверяет только уникальность имени интерфейса
	withRunningConfig(svc,
		"interface Wireguard0", "    security-level private", "!",
		"interface Wireguard1", "    ip access-group _WEBADMIN_Wireguard1 in", "!",
	)
	resetPosts(poster)
	svc.SweepForeignPermitAll(context.Background())
	want := []string{
		"no interface Wireguard1 ip access-group _WEBADMIN_Wireguard1 in",
		"no access-list _WEBADMIN_Wireguard1",
	}
	if got := parseStrings(poster); !slices.Equal(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
	withRunningConfig(svc, "interface Wireguard0", "!", "interface Wireguard1", "!")
	resetPosts(poster)
	svc.SweepForeignPermitAll(context.Background())
	if got := parseStrings(poster); len(got) != 0 {
		t.Fatalf("чистый роутер: RCI не должно быть, got %v", got)
	}
}
