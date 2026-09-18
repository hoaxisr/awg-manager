package managed

import (
	"context"
	"fmt"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/signature"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/sys/netif"
	"github.com/hoaxisr/awg-manager/internal/testing"
)

// GenerateConf generates a WireGuard client .conf file for a peer of the
// managed server identified by id. Непустой endpointHost подставляется в
// [Peer] Endpoint как есть — без обращения к WAN/KeenDNS: так конфиг просят
// прокси-обвязки (FreeTurn), которые всё равно ведут клиента на 127.0.0.1.
func (s *Service) GenerateConf(ctx context.Context, id, pubkey, endpointHost string) (string, error) {
	server, ok := s.settings.GetManagedServerByID(id)
	if !ok {
		return "", fmt.Errorf("managed server not found: %s", id)
	}

	idx := s.findPeerIndex(server, pubkey)
	if idx < 0 {
		return "", fmt.Errorf("peer not found: %s", pubkey)
	}
	peer := server.Peers[idx]

	// Get server public key from RCI
	serverInfo, err := s.queries.WGServers.Get(ctx, server.InterfaceName)
	if err != nil {
		return "", fmt.Errorf("get server info: %w", err)
	}
	serverPubKey := serverInfo.PublicKey
	if serverPubKey == "" {
		return "", fmt.Errorf("server public key not available")
	}

	// Resolve endpoint: explicit host → stored value → WAN IP
	endpoint := endpointHost
	if endpoint == "" {
		endpoint = server.Endpoint
	}
	if endpoint == "" {
		wanIP, err := testing.GetWANIPWithFallback(ctx, s.queries.WANInterfaceAddress)
		if err != nil {
			return "", fmt.Errorf("get WAN IP: %w", err)
		}
		endpoint = wanIP
	}

	// Резолвер: пир → сервер → LAN-адрес роутера. Зашитых публичных адресов
	// здесь больше нет (#933): абонент, которому мы отдали 1.1.1.1, резолвил
	// мимо роутера и мимо всех его правил DNS. Роутер не определился — строки
	// `DNS` в файле не будет вовсе: подставлять что-то «на всякий случай» —
	// ровно то, от чего уходим.
	dns := peer.DNS
	if dns == "" {
		dns = server.DNS
	}
	if dns == "" {
		dns = netif.RouterLANIP(storage.DefaultInterface)
	}
	mtu := effectiveMTU(server.MTU)

	// Numeric ASC params come from NDMS; the signature comes from the peer.
	ascRaw, _ := s.GetASCParams(ctx, id)

	// Build .conf
	var b strings.Builder

	b.WriteString("[Interface]\n")
	b.WriteString(fmt.Sprintf("PrivateKey = %s\n", peer.PrivateKey))
	b.WriteString(fmt.Sprintf("Address = %s\n", peer.TunnelIP))
	if dns != "" {
		b.WriteString(fmt.Sprintf("DNS = %s\n", dns))
	} else {
		// См. тот же случай в api/server_peers.go: `AllowedIPs` заворачивает
		// весь трафик, и файл без строки `DNS` оставляет клиента без резолва.
		s.appLog.Warn("peer-conf", pubkey,
			"LAN-адрес роутера не определился и свой DNS у пира не задан — в конфигурации не будет строки DNS")
	}
	b.WriteString(fmt.Sprintf("MTU = %d\n", mtu))

	// ASC params
	if ascRaw != nil {
		signature.WriteASCConf(&b, ascRaw, peerPackets(peer))
	}

	b.WriteString("\n[Peer]\n")
	b.WriteString(fmt.Sprintf("PublicKey = %s\n", serverPubKey))
	if peer.PresharedKey != "" {
		b.WriteString(fmt.Sprintf("PresharedKey = %s\n", peer.PresharedKey))
	}
	b.WriteString(fmt.Sprintf("Endpoint = %s:%d\n", endpoint, server.ListenPort))
	b.WriteString("AllowedIPs = 0.0.0.0/0, ::/0\n")
	b.WriteString("PersistentKeepalive = 25\n")

	return b.String(), nil
}

// peerPackets — сигнатура пира в виде, который понимает общий writer.
func peerPackets(peer storage.ManagedPeer) signature.GeneratedPackets {
	return signature.GeneratedPackets{I1: peer.I1, I2: peer.I2, I3: peer.I3, I4: peer.I4, I5: peer.I5}
}
