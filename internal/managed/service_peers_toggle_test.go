package managed

import (
	"context"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// Disabling a managed peer must carry its name into the RCI payload, else NDMS
// wipes the stored comment (the peer name on the router).
func TestTogglePeer_CarriesCommentToRCI(t *testing.T) {
	const pubkey = "katcNyLsEKRC9nxxyRLjD+Hh12bu57JjUECc9U37WzM="
	server := &storage.ManagedServer{
		InterfaceName: "Wireguard0",
		Peers: []storage.ManagedPeer{
			{PublicKey: pubkey, Description: "dacha", Enabled: true},
		},
	}
	svc, poster, store := newTestService(t, server, nil, `{}`)

	if err := svc.TogglePeer(context.Background(), "Wireguard0", pubkey, false); err != nil {
		t.Fatalf("TogglePeer: %v", err)
	}

	if len(poster.posts) != 1 {
		t.Fatalf("expected 1 RCI call, got %d", len(poster.posts))
	}
	peer := poster.posts[0]["interface"].(map[string]interface{})["Wireguard0"].(map[string]interface{})["wireguard"].(map[string]interface{})["peer"].([]map[string]interface{})[0]
	if peer["connect"] != false {
		t.Errorf("connect: %#v", peer["connect"])
	}
	if peer["comment"] != "dacha" {
		t.Errorf("comment not carried: %#v", peer["comment"])
	}

	persisted, ok := store.GetManagedServerByID("Wireguard0")
	if !ok || persisted.Peers[0].Enabled {
		t.Fatalf("enabled flag not persisted: %+v", persisted)
	}
}
