package wdtt

import (
	"context"
	"testing"
)

type fakeNDMSPeerLister struct {
	peers []NDMSPeerActivity
	err   error
}

func (f *fakeNDMSPeerLister) ListPeers(context.Context, string) ([]NDMSPeerActivity, error) {
	return f.peers, f.err
}

func TestActiveDevicesFromNDMSPeers(t *testing.T) {
	doc := passwordsJSON{
		Devices: map[string]any{
			"awgm-home": map[string]any{"ip": "10.66.0.5", "pub_key": "PK1"},
			"awgm-off":  map[string]any{"ip": "10.66.0.2", "pub_key": "PK2"},
		},
	}
	lister := &fakeNDMSPeerLister{peers: []NDMSPeerActivity{
		{PublicKey: "PK1", Online: true, LastHandshakeSecondsAgo: 15, Enabled: true},
		{PublicKey: "PK2", Online: false, LastHandshakeSecondsAgo: 600, Enabled: true},
	}}
	active := activeDevicesFromNDMSPeers(context.Background(), lister, "OpkgTun17", doc)
	if !active["awgm-home"] {
		t.Fatal("awgm-home should be active")
	}
	if active["awgm-off"] {
		t.Fatal("awgm-off should be inactive")
	}
}

func TestNdmsPeerRecentlyActive(t *testing.T) {
	if ndmsPeerRecentlyActive(NDMSPeerActivity{Online: true, Enabled: true, LastHandshakeSecondsAgo: 2147483647}) {
		t.Fatal("online without handshake must not count")
	}
	if !ndmsPeerRecentlyActive(NDMSPeerActivity{Online: true, LastHandshakeSecondsAgo: 30, Enabled: true}) {
		t.Fatal("recent handshake")
	}
	if ndmsPeerRecentlyActive(NDMSPeerActivity{LastHandshakeSecondsAgo: 600, Enabled: true}) {
		t.Fatal("stale handshake")
	}
}
