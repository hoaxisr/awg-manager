package storage

import "testing"

func TestMigrateToV36_ServerSignatureMovesToPeers(t *testing.T) {
	s := &Settings{SchemaVersion: 35, ManagedServers: []ManagedServer{{
		InterfaceName: "Wireguard1", LegacyI1: "<b 0x01>", LegacyI2: "<r 5>",
		Peers: []ManagedPeer{
			{PublicKey: "A", Description: "no own signature"},
			{PublicKey: "B", I1: "<b 0xff>", SignatureProfile: "dns"},
		},
	}}}
	(&SettingsStore{}).migrateToV36(s)
	sv := s.ManagedServers[0]
	if sv.LegacyI1 != "" || sv.LegacyI2 != "" {
		t.Fatal("legacy server fields must be cleared")
	}
	if sv.Peers[0].I1 != "<b 0x01>" || sv.Peers[0].I2 != "<r 5>" || sv.Peers[0].SignatureProfile != "" {
		t.Fatalf("peer A must inherit bytes verbatim, profile unknown: %+v", sv.Peers[0])
	}
	if sv.Peers[1].I1 != "<b 0xff>" || sv.Peers[1].SignatureProfile != "dns" {
		t.Fatalf("peer B keeps its own: %+v", sv.Peers[1])
	}
	// идемпотентность
	(&SettingsStore{}).migrateToV36(s)
	if s.ManagedServers[0].Peers[0].I1 != "<b 0x01>" {
		t.Fatal("second run must not change anything")
	}
}

func TestMigrateToV36_EndToEnd_RawJSON(t *testing.T) {
	s := loadFrom(t, `{"schemaVersion":35,"managedServers":[{"interfaceName":"Wireguard1","address":"10.0.0.1","mask":"255.255.255.0","listenPort":51820,"policy":"none","i1":"<b 0x0a>","i3":"<t>","peers":[{"publicKey":"P","privateKey":"k","presharedKey":"p","description":"d","tunnelIP":"10.0.0.2/32","enabled":true}]}]}`)
	if s.SchemaVersion != CurrentSchemaVersion {
		t.Fatalf("schema %d", s.SchemaVersion)
	}
	p := s.ManagedServers[0].Peers[0]
	if p.I1 != "<b 0x0a>" || p.I3 != "<t>" || p.I2 != "" {
		t.Fatalf("peer: %+v", p)
	}
}
