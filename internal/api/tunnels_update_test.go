package api

import (
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// TestMergeInterfaceWhitelist_PreservesAWGParams ensures that when the
// edit-form sends only Address/MTU/DNS, the AWG obfuscation parameters
// present in the existing tunnel are NOT silently overwritten with
// zeros (Bug H). Add new fields to the whitelist when the frontend
// starts editing them.
func TestMergeInterfaceWhitelist_PreservesAWGParams(t *testing.T) {
	existing := &storage.AWGTunnel{
		Interface: storage.AWGInterface{
			Address:    "10.0.0.1",
			MTU:        1420,
			DNS:        "1.1.1.1",
			PrivateKey: "secret",
			Qlen:       1000,
			Jc:         5, Jmin: 50, Jmax: 1000,
			S1: 100, S2: 200, S3: 300, S4: 400,
			H1: "h1val", H2: "h2val", H3: "h3val", H4: "h4val",
			I1: "i1val", I2: "i2val", I3: "i3val", I4: "i4val", I5: "i5val",
		},
	}
	req := &storage.AWGTunnel{
		Interface: storage.AWGInterface{
			Address: "10.0.0.2", // changed
			MTU:     1280,        // changed
			DNS:     "8.8.8.8",   // changed
			// PrivateKey, Qlen, AWG params NOT sent — must preserve
		},
	}
	mergeInterfaceWhitelist(req, existing)

	if req.Interface.Qlen != 1000 || req.Interface.Jc != 5 ||
		req.Interface.S1 != 100 || req.Interface.H1 != "h1val" ||
		req.Interface.I1 != "i1val" {
		t.Fatalf("AWG params lost: %+v", req.Interface)
	}
	if req.Interface.PrivateKey != "secret" {
		t.Fatalf("PrivateKey lost: got %q", req.Interface.PrivateKey)
	}
	if req.Interface.Address != "10.0.0.2" || req.Interface.MTU != 1280 || req.Interface.DNS != "8.8.8.8" {
		t.Fatalf("whitelist fields not applied: %+v", req.Interface)
	}
}

// TestMergeInterfaceWhitelist_PartialNoAddress preserves the entire
// Interface when Address is empty (routing-page partial update).
func TestMergeInterfaceWhitelist_PartialNoAddress(t *testing.T) {
	existing := &storage.AWGTunnel{
		Interface: storage.AWGInterface{Address: "10.0.0.1", MTU: 1420, DNS: "1.1.1.1", Qlen: 1000},
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
			PersistentKeepalive: 25,
		},
	}
	req := &storage.AWGTunnel{
		Peer: storage.AWGPeer{}, // empty — partial update
	}
	mergePeerWhitelist(req, existing)

	if req.Peer.PublicKey != "pubkey" || req.Peer.PresharedKey != "psk" ||
		req.Peer.Endpoint != "1.2.3.4:51820" || req.Peer.PersistentKeepalive != 25 ||
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
			PersistentKeepalive: 25,
		},
	}
	req := &storage.AWGTunnel{
		Peer: storage.AWGPeer{
			PublicKey:           "newkey",
			PresharedKey:        "newpsk",
			Endpoint:            "2.2.2.2:51820",
			AllowedIPs:          []string{"0.0.0.0/0"},
			PersistentKeepalive: 60,
		},
	}
	mergePeerWhitelist(req, existing)

	if req.Peer.PublicKey != "newkey" || req.Peer.PresharedKey != "newpsk" ||
		req.Peer.Endpoint != "2.2.2.2:51820" || req.Peer.PersistentKeepalive != 60 ||
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
