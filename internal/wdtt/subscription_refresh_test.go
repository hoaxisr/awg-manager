package wdtt

import "testing"

func TestFindProfileByPeer(t *testing.T) {
	profiles := []ImportPayload{
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
