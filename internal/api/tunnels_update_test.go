package api

import (
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// TestMergeInterfaceWhitelist_AppliesAWGParamsFromRequest covers issue #131:
// the full edit form sends new AWG obfuscation parameters (Jc, Jmin, S1-S4,
// H1-H4, I1-I5, Qlen) and the user expects them to land in storage. Earlier
// the whitelist always preserved AWG params from existing, silently
// discarding every UI edit; the regression manifested as "save" being a
// no-op for an Amnezia-Premium tunnel where the user wanted to clear or
// regenerate the I1 signature packet.
func TestMergeInterfaceWhitelist_AppliesAWGParamsFromRequest(t *testing.T) {
	existing := &storage.AWGTunnel{
		Interface: storage.AWGInterface{
			Address:    "10.0.0.1",
			MTU:        1420,
			DNS:        "1.1.1.1",
			PrivateKey: "secret",
			AWGObfuscation: storage.AWGObfuscation{
				Qlen: 1000,
				Jc:   5, Jmin: 50, Jmax: 1000,
				S1: 100, S2: 200, S3: 300, S4: 400,
				H1: "h1val", H2: "h2val", H3: "h3val", H4: "h4val",
				I1: "i1val", I2: "i2val", I3: "i3val", I4: "i4val", I5: "i5val",
			},
		},
	}
	req := &storage.AWGTunnel{
		Interface: storage.AWGInterface{
			Address: "10.0.0.1",
			MTU:     1420,
			DNS:     "1.1.1.1",
			AWGObfuscation: storage.AWGObfuscation{
				Qlen: 2000,
				Jc:   7, Jmin: 60, Jmax: 1100,
				S1: 110, S2: 210, S3: 310, S4: 410,
				H1: "new-h1", H2: "new-h2", H3: "new-h3", H4: "new-h4",
				I1: "new-i1", I2: "new-i2", I3: "new-i3", I4: "new-i4", I5: "new-i5",
			},
		},
	}
	mergeInterfaceWhitelist(req, existing)

	if req.Interface.Qlen != 2000 || req.Interface.Jc != 7 || req.Interface.Jmin != 60 ||
		req.Interface.Jmax != 1100 || req.Interface.S1 != 110 || req.Interface.S2 != 210 ||
		req.Interface.S3 != 310 || req.Interface.S4 != 410 {
		t.Fatalf("numeric AWG params not applied from req: %+v", req.Interface)
	}
	if req.Interface.H1 != "new-h1" || req.Interface.H2 != "new-h2" ||
		req.Interface.H3 != "new-h3" || req.Interface.H4 != "new-h4" {
		t.Fatalf("H1-H4 not applied from req: %+v", req.Interface)
	}
	if req.Interface.I1 != "new-i1" || req.Interface.I2 != "new-i2" ||
		req.Interface.I3 != "new-i3" || req.Interface.I4 != "new-i4" || req.Interface.I5 != "new-i5" {
		t.Fatalf("I1-I5 not applied from req: %+v", req.Interface)
	}
	// PrivateKey still preserves on empty (separate whitelist rule).
	if req.Interface.PrivateKey != "secret" {
		t.Fatalf("PrivateKey lost: got %q", req.Interface.PrivateKey)
	}
}

// TestMergeInterfaceWhitelist_ClearsAWGParamsFromRequest covers the
// "просто удалить i1" case from issue #131: the user explicitly empties
// signature packet fields in the edit form. The frontend sends i1=""
// (omitted via i1: undefined in buildUpdatePayload); the backend must
// honour that and persist the cleared value, not silently restore the
// previous one from existing.
func TestMergeInterfaceWhitelist_ClearsAWGParamsFromRequest(t *testing.T) {
	existing := &storage.AWGTunnel{
		Interface: storage.AWGInterface{
			Address: "10.0.0.1", MTU: 1420,
			AWGObfuscation: storage.AWGObfuscation{
				I1: "<r 2><b 0x8580...>", I2: "old-i2", I3: "old-i3",
			},
		},
	}
	req := &storage.AWGTunnel{
		Interface: storage.AWGInterface{
			Address: "10.0.0.1", MTU: 1420,
			// All I1-I5 omitted — JSON unmarshal leaves them empty.
		},
	}
	mergeInterfaceWhitelist(req, existing)

	if req.Interface.I1 != "" || req.Interface.I2 != "" || req.Interface.I3 != "" {
		t.Fatalf("I1-I3 should be cleared, got %+v", req.Interface)
	}
}

// TestMergeInterfaceWhitelist_PartialNoAddress preserves the entire
// Interface when Address is empty (routing-page partial update).
func TestMergeInterfaceWhitelist_PartialNoAddress(t *testing.T) {
	existing := &storage.AWGTunnel{
		Interface: storage.AWGInterface{Address: "10.0.0.1", MTU: 1420, DNS: "1.1.1.1", AWGObfuscation: storage.AWGObfuscation{Qlen: 1000}},
	}
	req := &storage.AWGTunnel{
		Interface: storage.AWGInterface{}, // empty — partial update
	}
	mergeInterfaceWhitelist(req, existing)

	if req.Interface.Address != "10.0.0.1" || req.Interface.MTU != 1420 || req.Interface.Qlen != 1000 {
		t.Fatalf("Interface not fully preserved: %+v", req.Interface)
	}
}

// TestMergeInterfaceWhitelist_NewPrivateKey allows replacing the
// PrivateKey when frontend explicitly sends a non-empty one (re-import
// or .conf replace flow).
func TestMergeInterfaceWhitelist_NewPrivateKey(t *testing.T) {
	existing := &storage.AWGTunnel{
		Interface: storage.AWGInterface{Address: "10.0.0.1", MTU: 1420, PrivateKey: "old"},
	}
	req := &storage.AWGTunnel{
		Interface: storage.AWGInterface{Address: "10.0.0.1", MTU: 1420, PrivateKey: "new"},
	}
	mergeInterfaceWhitelist(req, existing)

	if req.Interface.PrivateKey != "new" {
		t.Fatalf("PrivateKey not replaced: got %q", req.Interface.PrivateKey)
	}
}

// TestMergeInterfaceWhitelist_DNSCleared accepts an explicit empty DNS
// (user wants to remove DNS servers from the .conf).
func TestMergeInterfaceWhitelist_DNSCleared(t *testing.T) {
	existing := &storage.AWGTunnel{
		Interface: storage.AWGInterface{Address: "10.0.0.1", MTU: 1420, DNS: "1.1.1.1"},
	}
	req := &storage.AWGTunnel{
		Interface: storage.AWGInterface{Address: "10.0.0.1", MTU: 1420, DNS: ""},
	}
	mergeInterfaceWhitelist(req, existing)

	if req.Interface.DNS != "" {
		t.Fatalf("DNS not cleared: got %q", req.Interface.DNS)
	}
}

// TestMergePeerWhitelist_PreservesAllowedIPsOnPartial — when PublicKey
// is empty, the entire Peer preserves from existing.
func TestMergePeerWhitelist_PreservesAllowedIPsOnPartial(t *testing.T) {
	existing := &storage.AWGTunnel{
		Peer: storage.AWGPeer{
			PublicKey:           "pubkey",
			PresharedKey:        "psk",
			Endpoint:            "1.2.3.4:51820",
			AllowedIPs:          []string{"0.0.0.0/0", "::/0"},
			PersistentKeepalive: "25",
		},
	}
	req := &storage.AWGTunnel{
		Peer: storage.AWGPeer{}, // empty — partial update
	}
	mergePeerWhitelist(req, existing)

	if req.Peer.PublicKey != "pubkey" || req.Peer.PresharedKey != "psk" ||
		req.Peer.Endpoint != "1.2.3.4:51820" || req.Peer.PersistentKeepalive != "25" ||
		len(req.Peer.AllowedIPs) != 2 {
		t.Fatalf("Peer not fully preserved: %+v", req.Peer)
	}
}

// TestMergePeerWhitelist_AppliesAllFiveFields — when PublicKey is
// non-empty, all five whitelist fields apply from req.
func TestMergePeerWhitelist_AppliesAllFiveFields(t *testing.T) {
	existing := &storage.AWGTunnel{
		Peer: storage.AWGPeer{
			PublicKey:           "oldkey",
			PresharedKey:        "oldpsk",
			Endpoint:            "1.1.1.1:51820",
			AllowedIPs:          []string{"10.0.0.0/8"},
			PersistentKeepalive: "25",
		},
	}
	req := &storage.AWGTunnel{
		Peer: storage.AWGPeer{
			PublicKey:           "newkey",
			PresharedKey:        "newpsk",
			Endpoint:            "2.2.2.2:51820",
			AllowedIPs:          []string{"0.0.0.0/0"},
			PersistentKeepalive: "60",
		},
	}
	mergePeerWhitelist(req, existing)

	if req.Peer.PublicKey != "newkey" || req.Peer.PresharedKey != "newpsk" ||
		req.Peer.Endpoint != "2.2.2.2:51820" || req.Peer.PersistentKeepalive != "60" ||
		len(req.Peer.AllowedIPs) != 1 || req.Peer.AllowedIPs[0] != "0.0.0.0/0" {
		t.Fatalf("Peer fields not applied: %+v", req.Peer)
	}
}

// TestMergePeerWhitelist_PSKCleared lets the user remove the preshared
// key by explicitly sending empty PSK with non-empty PublicKey.
func TestMergePeerWhitelist_PSKCleared(t *testing.T) {
	existing := &storage.AWGTunnel{
		Peer: storage.AWGPeer{PublicKey: "k", PresharedKey: "psk"},
	}
	req := &storage.AWGTunnel{
		Peer: storage.AWGPeer{PublicKey: "k", PresharedKey: ""},
	}
	mergePeerWhitelist(req, existing)

	if req.Peer.PresharedKey != "" {
		t.Fatalf("PSK not cleared: got %q", req.Peer.PresharedKey)
	}
}
