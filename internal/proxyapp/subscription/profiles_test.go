package subscription

import (
	"testing"

	"github.com/hoaxisr/awg-manager/internal/proxyapp/wdttlink"
)

// Перенос subscription_refresh_test.go старого пакета.
func TestFindProfileByPeer(t *testing.T) {
	profiles := []wdttlink.ImportPayload{
		{Name: "DE", Peer: "1.2.3.4:56000", Password: "a", VKHashes: []string{"h"}},
		{Name: "FI", Peer: "5.6.7.8:56000", Password: "b", VKHashes: []string{"h"}},
	}
	got := FindProfileByPeer(profiles, "5.6.7.8:56000")
	if got == nil || got.Name != "FI" {
		t.Fatalf("got=%+v", got)
	}
	if FindProfileByPeer(profiles, "9.9.9.9:56000") != nil {
		t.Fatal("expected nil")
	}
}

func TestProfilesFromDecode(t *testing.T) {
	single := wdttlink.ImportPayload{Name: "DE", Peer: "1.2.3.4:56000"}
	got := ProfilesFromDecode(wdttlink.LinkDecodeResult{Profile: &single})
	if len(got) != 1 || got[0].Name != "DE" {
		t.Fatalf("одиночный профиль: %+v", got)
	}

	sub := wdttlink.SubscriptionPreview{Profiles: []wdttlink.ImportPayload{
		{Name: "DE", Peer: "1.2.3.4:56000"},
		{Name: "FI", Peer: "5.6.7.8:56000"},
	}}
	got = ProfilesFromDecode(wdttlink.LinkDecodeResult{Profile: &single, Subscription: &sub})
	if len(got) != 2 {
		t.Fatalf("подписка главнее одиночного профиля: %+v", got)
	}

	if ProfilesFromDecode(wdttlink.LinkDecodeResult{}) != nil {
		t.Fatal("пустой разбор — пустой список")
	}
}
