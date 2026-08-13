package wdtt

import (
	"context"
	"strings"
)

const ndmsPeerHandshakeMaxAgeSec int64 = 180

// NDMSPeerActivity is one WireGuard peer under an OpkgTun NDMS interface.
type NDMSPeerActivity struct {
	PublicKey               string
	Online                  bool
	LastHandshakeSecondsAgo int64
	Enabled                 bool
}

// NDMSPeerLister returns live peer stats for OpkgTun17..49 (Keenetic NDMS).
type NDMSPeerLister interface {
	ListPeers(ctx context.Context, ndmsIface string) ([]NDMSPeerActivity, error)
}

func ndmsPeerRecentlyActive(p NDMSPeerActivity) bool {
	if !p.Enabled {
		return false
	}
	ago := p.LastHandshakeSecondsAgo
	if ago >= 0 && ago < 2147483640 && ago <= ndmsPeerHandshakeMaxAgeSec {
		return true
	}
	return p.Online && ago >= 0 && ago < 2147483640 && ago <= ndmsPeerHandshakeMaxAgeSec
}

func devicePubKeyFromPasswordsEntry(v any) string {
	switch d := v.(type) {
	case map[string]any:
		for _, k := range []string{"pub_key", "pubKey", "public_key", "publicKey"} {
			if s, ok := d[k].(string); ok && strings.TrimSpace(s) != "" {
				return strings.TrimSpace(s)
			}
		}
	}
	return ""
}

func pubKeyToDeviceID(doc passwordsJSON) map[string]string {
	out := map[string]string{}
	for id, v := range doc.Devices {
		if pk := devicePubKeyFromPasswordsEntry(v); pk != "" {
			out[pk] = id
		}
	}
	return out
}

func activeDevicesFromNDMSPeers(ctx context.Context, lister NDMSPeerLister, ndmsIface string, doc passwordsJSON) map[string]bool {
	out := map[string]bool{}
	iface := strings.TrimSpace(ndmsIface)
	if lister == nil || iface == "" || !strings.HasPrefix(iface, "OpkgTun") {
		return out
	}
	peers, err := lister.ListPeers(ctx, iface)
	if err != nil {
		return out
	}
	byPub := pubKeyToDeviceID(doc)
	for _, p := range peers {
		if !ndmsPeerRecentlyActive(p) {
			continue
		}
		pk := strings.TrimSpace(p.PublicKey)
		if id := byPub[pk]; id != "" {
			out[id] = true
		}
	}
	return out
}
