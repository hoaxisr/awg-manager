package payloads

import "github.com/hoaxisr/awg-manager/internal/ndms/query"

// PeerConfig holds WireGuard peer parameters for CmdWireguardPeer.
type PeerConfig struct {
	PublicKey         string
	Endpoint          string
	AllowedIPv4       []AllowedIP
	AllowedIPv6       []AllowedIP
	KeepaliveInterval int
	PresharedKey      string
}

// AllowedIP is an address/mask pair for WireGuard allowed-ips.
type AllowedIP struct {
	Address string
	Mask    string
}

// --- Interface basic (#9, #29, #10, #11, #25, #26) ---

func CmdInterfaceCreate(name string) any {
	return map[string]any{"interface": map[string]any{"name": name}}
}

func CmdInterfaceDelete(c query.Confirmed) any {
	return map[string]any{"interface": map[string]any{"name": c.Name(), "no": true}}
}

func CmdInterfaceDescription(c query.Confirmed, desc string) any {
	return map[string]any{"interface": map[string]any{"name": c.Name(), "description": desc}}
}

func CmdInterfaceSecurityLevel(c query.Confirmed, level string) any {
	return map[string]any{"interface": map[string]any{"name": c.Name(), "security-level": map[string]any{level: true}}}
}

func CmdInterfaceUp(c query.Confirmed, up bool) any {
	return map[string]any{"interface": map[string]any{"name": c.Name(), "up": up}}
}

// --- IP config (#12, #13, #14, #15) ---

func CmdInterfaceIPAddress(c query.Confirmed, address, mask string) any {
	return map[string]any{"interface": map[string]any{"name": c.Name(), "ip": map[string]any{
		"address": map[string]any{"address": address, "mask": mask},
	}}}
}

func CmdInterfaceMTU(c query.Confirmed, mtu int) any {
	return map[string]any{"interface": map[string]any{"name": c.Name(), "ip": map[string]any{"mtu": mtu}}}
}

func CmdInterfaceAdjustMSS(c query.Confirmed, enable bool) any {
	return map[string]any{"interface": map[string]any{"name": c.Name(), "ip": map[string]any{"adjust-mss": enable}}}
}

func CmdInterfaceIPGlobal(c query.Confirmed, auto bool) any {
	var global any
	if auto {
		global = map[string]any{"auto": true}
	} else {
		global = map[string]any{}
	}
	return map[string]any{"interface": map[string]any{"name": c.Name(), "ip": map[string]any{"global": global}}}
}

// --- DNS (#17, #28) ---

func CmdInterfaceDNS(c query.Confirmed, servers []string) any {
	var list []any
	for _, s := range servers {
		list = append(list, map[string]any{"name-server": s})
	}
	return map[string]any{"interface": map[string]any{"name": c.Name(), "ip": map[string]any{"name-server": list}}}
}

// --- IPv6 (#3, #4, #18) ---

func CmdInterfaceIPv6Address(c query.Confirmed, address string) any {
	return map[string]any{"interface": map[string]any{"name": c.Name(), "ipv6": map[string]any{
		"address": []any{map[string]any{"block": address + "/128"}},
	}}}
}

// --- WireGuard (#16, #19-23, #24, #27) ---

func CmdWireguardPrivateKey(c query.Confirmed, key string) any {
	return map[string]any{"interface": map[string]any{"name": c.Name(), "wireguard": map[string]any{"private-key": key}}}
}

func CmdWireguardPeer(c query.Confirmed, peer PeerConfig) any {
	p := map[string]any{"key": peer.PublicKey}
	if peer.Endpoint != "" {
		p["endpoint"] = map[string]any{"address": peer.Endpoint}
	}
	var allowIPs []any
	for _, ip := range peer.AllowedIPv4 {
		allowIPs = append(allowIPs, map[string]any{"address": ip.Address, "mask": ip.Mask})
	}
	for _, ip := range peer.AllowedIPv6 {
		allowIPs = append(allowIPs, map[string]any{"address": ip.Address, "mask": ip.Mask})
	}
	if len(allowIPs) > 0 {
		p["allow-ips"] = allowIPs
	}
	if peer.KeepaliveInterval > 0 {
		p["keepalive-interval"] = map[string]any{"interval": peer.KeepaliveInterval}
	}
	if peer.PresharedKey != "" {
		p["preshared-key"] = peer.PresharedKey
	}
	return map[string]any{"interface": map[string]any{"name": c.Name(), "wireguard": map[string]any{"peer": p}}}
}

func CmdWireguardPeerEndpoint(c query.Confirmed, publicKey, endpoint string) any {
	return map[string]any{"interface": map[string]any{"name": c.Name(), "wireguard": map[string]any{
		"peer": map[string]any{"key": publicKey, "endpoint": map[string]any{"address": endpoint}},
	}}}
}

func CmdWireguardPeerConnect(c query.Confirmed, publicKey, viaInterface string) any {
	return map[string]any{"interface": map[string]any{"name": c.Name(), "wireguard": map[string]any{
		"peer": map[string]any{"key": publicKey, "connect": map[string]any{"via": viaInterface}},
	}}}
}

func CmdWireguardPeerDisconnect(c query.Confirmed, publicKey string) any {
	return map[string]any{"interface": map[string]any{"name": c.Name(), "wireguard": map[string]any{
		"peer": map[string]any{"key": publicKey, "connect": map[string]any{"no": true}},
	}}}
}

// CmdWireguardPeerNo removes a peer from the WireGuard interface entirely
// (deletes the peer entry, not just disconnects it). Verified RCI shape:
// `{"peer": {"key": "...", "no": true}}` returns
// `Wireguard::Interface: removed peer "..."` from NDMS.
func CmdWireguardPeerNo(c query.Confirmed, publicKey string) any {
	return map[string]any{"interface": map[string]any{"name": c.Name(), "wireguard": map[string]any{
		"peer": map[string]any{"key": publicKey, "no": true},
	}}}
}

// --- System (#30) ---

func CmdSave() any {
	return map[string]any{"system": map[string]any{"configuration": map[string]any{"save": map[string]any{}}}}
}
