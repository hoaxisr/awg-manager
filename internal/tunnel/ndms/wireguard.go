package ndms

import (
	"fmt"
	"math"
	"net"
	"time"
)

const noHandshakeMarker = math.MaxInt32 // 2147483647

// SystemWireguardTunnel holds full info about a system WireGuard interface.
type SystemWireguardTunnel struct {
	ID            string             `json:"id"`            // NDMS name: "Wireguard0"
	InterfaceName string             `json:"interfaceName"` // kernel name: "nwg0"
	Description   string             `json:"description"`
	Status        string             `json:"status"`        // "up" / "down"
	Connected     bool               `json:"connected"`
	MTU           int                `json:"mtu"`
	Peer          *WireguardPeerInfo `json:"peer,omitempty"`
}

// WireguardPeerInfo holds peer details from RCI show interface.
type WireguardPeerInfo struct {
	PublicKey     string `json:"publicKey"`
	Endpoint      string `json:"endpoint"`      // "ip:port"
	RxBytes       int64  `json:"rxBytes"`
	TxBytes       int64  `json:"txBytes"`
	LastHandshake string `json:"lastHandshake"` // RFC3339 or ""
	Online        bool   `json:"online"`
}

// WireguardServer represents a WireGuard server interface with all peers.
type WireguardServer struct {
	ID            string                `json:"id"`
	InterfaceName string                `json:"interfaceName"`
	Description   string                `json:"description"`
	Status        string                `json:"status"`
	Connected     bool                  `json:"connected"`
	MTU           int                   `json:"mtu"`
	Address       string                `json:"address"`
	Mask          string                `json:"mask"`
	PublicKey     string                `json:"publicKey"`
	ListenPort    int                   `json:"listenPort"`
	Peers         []WireguardServerPeer `json:"peers"`
}

// WireguardServerPeer holds per-peer runtime data for a server.
type WireguardServerPeer struct {
	PublicKey     string `json:"publicKey"`
	Description   string `json:"description"`
	Endpoint      string `json:"endpoint"`
	RxBytes       int64  `json:"rxBytes"`
	TxBytes       int64  `json:"txBytes"`
	LastHandshake string `json:"lastHandshake"`
	Online        bool   `json:"online"`
	Enabled       bool   `json:"enabled"`
}

// WireguardServerConfig holds configuration data for .conf generation.
type WireguardServerConfig struct {
	PublicKey  string                       `json:"publicKey"`
	ListenPort int                          `json:"listenPort"`
	MTU        int                          `json:"mtu"`
	Address    string                       `json:"address"`
	Peers      []WireguardServerPeerConfig  `json:"peers"`
}

// WireguardServerPeerConfig holds per-peer config data from RC.
type WireguardServerPeerConfig struct {
	PublicKey    string   `json:"publicKey"`
	Description  string   `json:"description"`
	PresharedKey string   `json:"presharedKey"`
	AllowedIPs   []string `json:"allowedIPs"`
	Address      string   `json:"address"` // peer tunnel IP (first /32 from allow-ips)
}

// rciWireguardDetail extends rciInterfaceInfo with wireguard-specific peer data.
// Used to parse /show/interface/WireguardX which includes the "wireguard" nested object.
type rciWireguardDetail struct {
	rciInterfaceInfo
	MTU       int `json:"mtu"`
	Wireguard *struct {
		PublicKey  string             `json:"public-key"`
		ListenPort int               `json:"listen-port"`
		Peer      []rciWireguardPeer `json:"peer"`
	} `json:"wireguard"`
}

type rciWireguardPeer struct {
	PublicKey             string `json:"public-key"`
	Description           string `json:"description"`
	RemoteEndpointAddress string `json:"remote-endpoint-address"`
	RemotePort            int    `json:"remote-port"`
	RxBytes               int64  `json:"rxbytes"`
	TxBytes               int64  `json:"txbytes"`
	LastHandshake         int64  `json:"last-handshake"`
	Online                bool   `json:"online"`
	Enabled               bool   `json:"enabled"`
}

// rciRCInterface parses /show/rc/interface/X response.
type rciRCInterface struct {
	Description string `json:"description"`
	IP          *struct {
		Address *struct {
			Address string `json:"address"`
			Mask    string `json:"mask"`
		} `json:"address"`
		MTU string `json:"mtu"`
	} `json:"ip"`
	Wireguard *struct {
		ListenPort *struct {
			Port int `json:"port"`
		} `json:"listen-port"`
		Peer []rciRCPeer `json:"peer"`
	} `json:"wireguard"`
}

type rciRCPeer struct {
	Key          string `json:"key"`
	Comment      string `json:"comment"`
	PresharedKey string `json:"preshared-key"`
	AllowIPs     []struct {
		Address string `json:"address"`
		Mask    string `json:"mask"`
	} `json:"allow-ips"`
}

// formatHandshakeSecondsAgo converts RCI last-handshake (seconds ago) to RFC3339 or "".
// RCI returns seconds since last handshake, not a unix timestamp.
// Value 0 or >= MaxInt32 means no handshake has occurred.
func FormatHandshakeSecondsAgo(secsAgo int64) string {
	if secsAgo <= 0 || secsAgo >= int64(noHandshakeMarker) {
		return ""
	}
	return time.Now().Add(-time.Duration(secsAgo) * time.Second).Format(time.RFC3339)
}

// formatPeerEndpoint formats endpoint from RCI peer data.
func formatPeerEndpoint(p rciWireguardPeer) string {
	if p.RemoteEndpointAddress == "" && p.RemotePort == 0 {
		return ""
	}
	return fmt.Sprintf("%s:%d", p.RemoteEndpointAddress, p.RemotePort)
}

// rciToSystemTunnel converts RCI interface data to SystemWireguardTunnel.
func rciToSystemTunnel(iface rciWireguardDetail) SystemWireguardTunnel {
	t := SystemWireguardTunnel{
		ID:          iface.InterfaceName,
		Description: iface.Description,
		Status:      iface.State,
		Connected:   iface.Connected == "yes",
		MTU:         iface.MTU,
	}

	if iface.Wireguard != nil && len(iface.Wireguard.Peer) > 0 {
		peer := iface.Wireguard.Peer[0]
		t.Peer = &WireguardPeerInfo{
			PublicKey:     peer.PublicKey,
			Endpoint:      formatPeerEndpoint(peer),
			RxBytes:       peer.RxBytes,
			TxBytes:       peer.TxBytes,
			LastHandshake: FormatHandshakeSecondsAgo(peer.LastHandshake),
			Online:        peer.Online,
		}
	}
	return t
}

// rciToWireguardServer converts RCI interface data to WireguardServer with all peers.
func rciToWireguardServer(iface rciWireguardDetail) WireguardServer {
	server := WireguardServer{
		ID:          iface.InterfaceName,
		Description: iface.Description,
		Status:      iface.State,
		Connected:   iface.Connected == "yes",
		MTU:         iface.MTU,
		Address:     iface.Address,
		Mask:        iface.Mask,
	}
	if iface.Wireguard != nil {
		server.PublicKey = iface.Wireguard.PublicKey
		server.ListenPort = iface.Wireguard.ListenPort
		for _, p := range iface.Wireguard.Peer {
			peer := WireguardServerPeer{
				PublicKey:     p.PublicKey,
				Description:   p.Description,
				Endpoint:      formatPeerEndpoint(p),
				RxBytes:       p.RxBytes,
				TxBytes:       p.TxBytes,
				LastHandshake: FormatHandshakeSecondsAgo(p.LastHandshake),
				Online:        p.Online,
				Enabled:       p.Enabled,
			}
			server.Peers = append(server.Peers, peer)
		}
	}
	return server
}

// rciRCToServerConfig converts RC config data to WireguardServerConfig.
func rciRCToServerConfig(rc rciRCInterface, publicKey string) WireguardServerConfig {
	cfg := WireguardServerConfig{
		PublicKey: publicKey,
	}
	if rc.IP != nil {
		if rc.IP.Address != nil {
			cfg.Address = rc.IP.Address.Address
		}
		if rc.IP.MTU != "" {
			fmt.Sscanf(rc.IP.MTU, "%d", &cfg.MTU)
		}
	}
	if rc.Wireguard != nil {
		if rc.Wireguard.ListenPort != nil {
			cfg.ListenPort = rc.Wireguard.ListenPort.Port
		}
		for _, p := range rc.Wireguard.Peer {
			peer := WireguardServerPeerConfig{
				PublicKey:    p.Key,
				Description:  p.Comment,
				PresharedKey: p.PresharedKey,
			}
			for _, aip := range p.AllowIPs {
				ip := net.ParseIP(aip.Mask)
				if ip == nil {
					continue
				}
				ip4 := ip.To4()
				if ip4 == nil {
					continue
				}
				mask := net.IPMask(ip4)
				ones, _ := mask.Size()
				peer.AllowedIPs = append(peer.AllowedIPs, fmt.Sprintf("%s/%d", aip.Address, ones))
				// First /32 entry is the peer's tunnel IP
				if ones == 32 && peer.Address == "" {
					peer.Address = aip.Address
				}
			}
			cfg.Peers = append(cfg.Peers, peer)
		}
	}
	return cfg
}
