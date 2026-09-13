package managed

import (
	"context"
	"slices"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/logging"
)

// Running-config недоступен → сегменты всё равно применяются (4 прежние
// команды + auto-delete), и никакого Warn: ради ACL мы в running-config
// больше не ходим — чужой `_WEBADMIN_` не снимается (#879).
func TestApplyLANSegments_RunningConfigUnavailable_ProceedsSilently(t *testing.T) {
	svc, store, poster := newLANSegmentsTestService(t) // stateAwareGetter: running-config = ошибка
	spy := &recAppLog{}
	svc.appLog = logging.NewScopedLogger(spy, logging.GroupServer, logging.SubManaged)
	seedServer(t, store, "Wireguard0")
	resetPosts(poster)
	if err := svc.SetLANSegments(context.Background(), "Wireguard0", []string{"Home"}); err != nil {
		t.Fatal(err)
	}
	if n := len(parseStrings(poster)); n != 5 {
		t.Fatalf("команд %d, ждали 5", n)
	}
	if len(spy.entries) != 1 || spy.entries[0] != "info|lan-segments|Wireguard0|LAN segments changed: Home" {
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

// Чужой `_WEBADMIN_<iface>` не трогает НИ ОДИН путь применения сегментов:
// это правила межсетевого экрана пользователя из веб-морды роутера (список
// именуется по интерфейсу и держит все его строки, не только permit-all).
// Прежний код снимал его целиком по имени — issue #879: правило пропадало
// после каждой перезагрузки роутера.
func TestApplyLANSegments_NeverTouchesForeignPermitAll(t *testing.T) {
	want := []string{
		"no interface Wireguard0 ip access-group AWGM_Wireguard0 in",
		"no access-list AWGM_Wireguard0",
		"access-list AWGM_Wireguard0 permit ip 10.66.66.0 255.255.255.0 10.10.10.0 255.255.255.0",
		"interface Wireguard0 ip access-group AWGM_Wireguard0 in",
		"access-list AWGM_Wireguard0 auto-delete",
	}
	for _, tc := range []struct {
		name  string
		apply func(*Service) error
	}{
		{"фасад ndms_access роли wdtt", func(svc *Service) error {
			return svc.ApplyLANSegmentsToInterface(context.Background(), "Wireguard0", "10.66.66.1", "255.255.255.0", []string{"Home"})
		}},
		{"карточка встроенного сервера", func(svc *Service) error {
			return svc.SetLANSegments(context.Background(), "Wireguard0", []string{"Home"})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, store, poster := newLANSegmentsTestService(t)
			seedServer(t, store, "Wireguard0")
			withRunningConfig(svc,
				"interface Wireguard0",
				"    security-level private",
				"    ip access-group _WEBADMIN_Wireguard0 in",
				"!",
			)
			resetPosts(poster)
			if err := tc.apply(svc); err != nil {
				t.Fatal(err)
			}
			got := parseStrings(poster)
			if !slices.Equal(got, want) {
				t.Fatalf("got %v want %v", got, want)
			}
		})
	}
}

// Без стора running-config ForeignAccessGroups отказывает и в RCI не ходит:
// молчаливое «чужих нет» скрыло бы и остаток `_WEBADMIN_`, и чужой список в
// карточке сервера.
func TestForeignAccessGroups_NoStore_ErrorsWithoutRCI(t *testing.T) {
	svc, _, poster := newLANSegmentsTestService(t)
	svc.queries = nil
	resetPosts(poster)
	_, err := svc.ForeignAccessGroups(context.Background(), "Wireguard0")
	if err == nil || err.Error() != "running-config store not wired" {
		t.Fatalf("err = %v, ждали «running-config store not wired»", err)
	}
	if got := parseStrings(poster); len(got) != 0 {
		t.Fatalf("RCI не должно быть, got %v", got)
	}
}
