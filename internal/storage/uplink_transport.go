package storage

import "reflect"

// SameUplinkTransport excludes status, labels, monitoring and display name.
func SameUplinkTransport(a, b *AWGTunnel) bool {
	return a != nil && b != nil && a.ID == b.ID && a.Backend == b.Backend && a.NWGIndex == b.NWGIndex && a.ISPInterface == b.ISPInterface && reflect.DeepEqual(a.Interface, b.Interface) && reflect.DeepEqual(a.Peer, b.Peer) && reflect.DeepEqual(a.Obfuscator, b.Obfuscator)
}
