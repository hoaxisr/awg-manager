package rci

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

func CmdInterfaceDelete(name string) any {
	return map[string]any{"interface": map[string]any{"name": name, "no": true}}
}

func CmdInterfaceDescription(name, desc string) any {
	return map[string]any{"interface": map[string]any{"name": name, "description": desc}}
}

func CmdInterfaceSecurityLevel(name, level string) any {
	return map[string]any{"interface": map[string]any{"name": name, "security-level": map[string]any{level: true}}}
}

func CmdInterfaceUp(name string, up bool) any {
	return map[string]any{"interface": map[string]any{"name": name, "up": up}}
}

// --- IP config (#12, #13, #14, #15) ---

func CmdInterfaceIPAddress(name, address, mask string) any {
	return map[string]any{"interface": map[string]any{"name": name, "ip": map[string]any{
		"address": map[string]any{"address": address, "mask": mask},
	}}}
}

func CmdInterfaceMTU(name string, mtu int) any {
	return map[string]any{"interface": map[string]any{"name": name, "ip": map[string]any{"mtu": mtu}}}
}

func CmdInterfaceAdjustMSS(name string, enable bool) any {
	return map[string]any{"interface": map[string]any{"name": name, "ip": map[string]any{"adjust-mss": enable}}}
}

func CmdInterfaceIPGlobal(name string, auto bool) any {
	var global any
	if auto {
		global = map[string]any{"auto": true}
	} else {
		global = map[string]any{}
	}
	return map[string]any{"interface": map[string]any{"name": name, "ip": map[string]any{"global": global}}}
}

// --- DNS (#17, #28) ---

func CmdInterfaceDNS(name string, servers []string) any {
	var list []any
	for _, s := range servers {
		list = append(list, map[string]any{"name-server": s})
	}
	return map[string]any{"interface": map[string]any{"name": name, "ip": map[string]any{"name-server": list}}}
}

func CmdInterfaceDNSClear(name string) any {
	return map[string]any{"interface": map[string]any{"name": name, "ip": map[string]any{"name-server": map[string]any{}}}}
}

// --- IPv6 (#3, #4, #18) ---

func CmdInterfaceIPv6Address(name, address string) any {
	return map[string]any{"interface": map[string]any{"name": name, "ipv6": map[string]any{
		"address": []any{map[string]any{"block": address + "/128"}},
	}}}
}

func CmdInterfaceIPv6AddressClear(name string) any {
	return map[string]any{"interface": map[string]any{"name": name, "ipv6": map[string]any{"address": map[string]any{}}}}
}

// --- WireGuard (#16, #19-23, #24, #27) ---

func CmdWireguardPrivateKey(name, key string) any {
	return map[string]any{"interface": map[string]any{"name": name, "wireguard": map[string]any{"private-key": key}}}
}

func CmdWireguardPeer(name string, peer PeerConfig) any {
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
	return map[string]any{"interface": map[string]any{"name": name, "wireguard": map[string]any{"peer": p}}}
}

func CmdWireguardPeerDelete(name, publicKey string) any {
	return map[string]any{"interface": map[string]any{"name": name, "wireguard": map[string]any{
		"peer": map[string]any{"key": publicKey, "no": true},
	}}}
}

func CmdWireguardPeerEndpoint(name, publicKey, endpoint string) any {
	return map[string]any{"interface": map[string]any{"name": name, "wireguard": map[string]any{
		"peer": map[string]any{"key": publicKey, "endpoint": map[string]any{"address": endpoint}},
	}}}
}

func CmdWireguardPeerConnect(name, publicKey, viaInterface string) any {
	return map[string]any{"interface": map[string]any{"name": name, "wireguard": map[string]any{
		"peer": map[string]any{"key": publicKey, "connect": map[string]any{"via": viaInterface}},
	}}}
}

func CmdWireguardPeerDisconnect(name, publicKey string) any {
	return map[string]any{"interface": map[string]any{"name": name, "wireguard": map[string]any{
		"peer": map[string]any{"key": publicKey, "connect": map[string]any{"no": true}},
	}}}
}

// --- IPv4 routes (#1, #2) ---

func CmdSetDefaultRoute(name string) any {
	return map[string]any{"ip": map[string]any{"route": map[string]any{"default": true, "interface": name}}}
}

func CmdRemoveDefaultRoute(name string) any {
	return map[string]any{"ip": map[string]any{"route": map[string]any{"default": true, "interface": name, "no": true}}}
}

// --- IPv6 routes (#5, #6, #7) ---

func CmdSetIPv6DefaultRoute(name string) any {
	return map[string]any{"ipv6": map[string]any{"route": map[string]any{"default": true, "interface": name}}}
}

func CmdRemoveIPv6DefaultRoute(name string) any {
	return map[string]any{"ipv6": map[string]any{"route": map[string]any{"default": true, "interface": name, "no": true}}}
}

func CmdRemoveIPv6HostRoute(host string) any {
	return map[string]any{"ipv6": map[string]any{"route": map[string]any{"host": host, "no": true}}}
}

// --- System (#30) ---

func CmdSave() any {
	return map[string]any{"system": map[string]any{"configuration": map[string]any{"save": map[string]any{}}}}
}
