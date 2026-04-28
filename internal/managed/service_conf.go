package managed

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/testing"
)

// GenerateConf generates a WireGuard client .conf file for a peer of the
// managed server identified by id.
func (s *Service) GenerateConf(ctx context.Context, id, pubkey string) (string, error) {
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

	// Resolve endpoint: use stored value or fall back to WAN IP
	endpoint := server.Endpoint
	if endpoint == "" {
		wanIP, err := testing.GetWANIPWithFallback(ctx, s.queries.WANInterfaceAddress)
		if err != nil {
			return "", fmt.Errorf("get WAN IP: %w", err)
		}
		endpoint = wanIP
	}

	// Resolve DNS: per-peer → server-level → default
	dns := peer.DNS
	if dns == "" {
		dns = server.DNS
	}
	if dns == "" {
		dns = "1.1.1.1, 8.8.8.8"
	}
	mtu := server.MTU
	if mtu == 0 {
		mtu = 1376
	}

	// Get ASC params from NDMS + locally stored I1-I5
	ascRaw, _ := s.GetASCParams(ctx, id)

	// Build .conf
	var b strings.Builder

	b.WriteString("[Interface]\n")
	b.WriteString(fmt.Sprintf("PrivateKey = %s\n", peer.PrivateKey))
	b.WriteString(fmt.Sprintf("Address = %s\n", peer.TunnelIP))
	b.WriteString(fmt.Sprintf("DNS = %s\n", dns))
	b.WriteString(fmt.Sprintf("MTU = %d\n", mtu))

	// ASC params
	if ascRaw != nil {
		writeASCParams(&b, ascRaw)
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

// writeASCParams writes ASC parameters to the .conf [Interface] section.
func writeASCParams(b *strings.Builder, raw json.RawMessage) {
	var ext ndms.ASCParamsExtended
	if err := json.Unmarshal(raw, &ext); err != nil || ext.Jc == 0 {
		return
	}

	b.WriteString(fmt.Sprintf("Jc = %d\n", ext.Jc))
	b.WriteString(fmt.Sprintf("Jmin = %d\n", ext.Jmin))
	b.WriteString(fmt.Sprintf("Jmax = %d\n", ext.Jmax))
	b.WriteString(fmt.Sprintf("S1 = %d\n", ext.S1))
	b.WriteString(fmt.Sprintf("S2 = %d\n", ext.S2))
	b.WriteString(fmt.Sprintf("H1 = %s\n", ext.H1))
	b.WriteString(fmt.Sprintf("H2 = %s\n", ext.H2))
	b.WriteString(fmt.Sprintf("H3 = %s\n", ext.H3))
	b.WriteString(fmt.Sprintf("H4 = %s\n", ext.H4))

	if ext.S3 > 0 || ext.S4 > 0 {
		b.WriteString(fmt.Sprintf("S3 = %d\n", ext.S3))
		b.WriteString(fmt.Sprintf("S4 = %d\n", ext.S4))
	}
	if ext.I1 != "" {
		b.WriteString(fmt.Sprintf("I1 = %s\n", ext.I1))
	}
	if ext.I2 != "" {
		b.WriteString(fmt.Sprintf("I2 = %s\n", ext.I2))
	}
	if ext.I3 != "" {
		b.WriteString(fmt.Sprintf("I3 = %s\n", ext.I3))
	}
	if ext.I4 != "" {
		b.WriteString(fmt.Sprintf("I4 = %s\n", ext.I4))
	}
	if ext.I5 != "" {
		b.WriteString(fmt.Sprintf("I5 = %s\n", ext.I5))
	}
}
