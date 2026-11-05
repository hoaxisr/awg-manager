package managed

import (
	"context"
	"slices"
	"testing"
)

// Чужой `_WEBADMIN_<iface>` не трогает НИ ОДИН путь применения сегментов:
// это правила межсетевого экрана пользователя из веб-морды роутера (список
// именуется по интерфейсу и держит все его строки, не только permit-all).
// Прежний код снимал его целиком по имени — issue #879: правило пропадало
// после каждой перезагрузки роутера.
func TestApplyLANSegments_NeverTouchesForeignPermitAll(t *testing.T) {
	want := []string{
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
