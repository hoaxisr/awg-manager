// Package config provides WireGuard configuration parsing and generation.
package config

import (
	"bufio"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// Default values for tunnel configuration.
const (
	DefaultMTU                 = 1280
	DefaultPersistentKeepalive = 25
)

// Configuration parsing errors.
var (
	ErrMultiplePeers     = errors.New("multiple peers not supported")
	ErrMissingPrivateKey = errors.New("missing PrivateKey in [Interface]")
	ErrMissingAddress    = errors.New("missing Address in [Interface]")
	ErrMissingPublicKey  = errors.New("missing PublicKey in [Peer]")
	ErrMissingEndpoint   = errors.New("missing Endpoint in [Peer]")
)

// DefaultAllowedIPs returns the default AllowedIPs for full tunnel routing.
func DefaultAllowedIPs() []string {
	return []string{"0.0.0.0/0", "::/0"}
}

// Generate generates WireGuard .conf content from tunnel metadata.
func Generate(tunnel *storage.AWGTunnel) string {
	var b strings.Builder

	b.WriteString("[Interface]\n")
	b.WriteString(fmt.Sprintf("PrivateKey = %s\n", tunnel.Interface.PrivateKey))

	iface := &tunnel.Interface

	if iface.Jc > 0 {
		b.WriteString(fmt.Sprintf("Jc = %d\n", iface.Jc))
	}
	if iface.Jmin > 0 {
		b.WriteString(fmt.Sprintf("Jmin = %d\n", iface.Jmin))
	}
	if iface.Jmax > 0 {
		b.WriteString(fmt.Sprintf("Jmax = %d\n", iface.Jmax))
	}

	if iface.S1 > 0 {
		b.WriteString(fmt.Sprintf("S1 = %d\n", iface.S1))
	}
	if iface.S2 > 0 {
		b.WriteString(fmt.Sprintf("S2 = %d\n", iface.S2))
	}
	if iface.S3 > 0 {
		b.WriteString(fmt.Sprintf("S3 = %d\n", iface.S3))
	}
	if iface.S4 > 0 {
		b.WriteString(fmt.Sprintf("S4 = %d\n", iface.S4))
	}

	if iface.H1 != "" {
		b.WriteString(fmt.Sprintf("H1 = %s\n", iface.H1))
	}
	if iface.H2 != "" {
		b.WriteString(fmt.Sprintf("H2 = %s\n", iface.H2))
	}
	if iface.H3 != "" {
		b.WriteString(fmt.Sprintf("H3 = %s\n", iface.H3))
	}
	if iface.H4 != "" {
		b.WriteString(fmt.Sprintf("H4 = %s\n", iface.H4))
	}

	if iface.I1 != "" {
		b.WriteString(fmt.Sprintf("I1 = %s\n", iface.I1))
	}
	if iface.I2 != "" {
		b.WriteString(fmt.Sprintf("I2 = %s\n", iface.I2))
	}
	if iface.I3 != "" {
		b.WriteString(fmt.Sprintf("I3 = %s\n", iface.I3))
	}
	if iface.I4 != "" {
		b.WriteString(fmt.Sprintf("I4 = %s\n", iface.I4))
	}
	if iface.I5 != "" {
		b.WriteString(fmt.Sprintf("I5 = %s\n", iface.I5))
	}

	b.WriteString("\n[Peer]\n")
	b.WriteString(fmt.Sprintf("PublicKey = %s\n", tunnel.Peer.PublicKey))
	if tunnel.Peer.PresharedKey != "" {
		b.WriteString(fmt.Sprintf("PresharedKey = %s\n", tunnel.Peer.PresharedKey))
	}

	allowedIPs := tunnel.Peer.AllowedIPs
	if len(allowedIPs) == 0 {
		allowedIPs = DefaultAllowedIPs()
	}
	b.WriteString(fmt.Sprintf("AllowedIPs = %s\n", strings.Join(allowedIPs, ", ")))

	b.WriteString(fmt.Sprintf("Endpoint = %s\n", tunnel.Peer.Endpoint))

	keepalive := tunnel.Peer.PersistentKeepalive
	if keepalive == 0 {
		keepalive = DefaultPersistentKeepalive
	}
	b.WriteString(fmt.Sprintf("PersistentKeepalive = %d\n", keepalive))

	return b.String()
}

// GenerateForExport generates a client-compatible .conf for user export/download.
// Unlike Generate(), it includes Address and MTU in [Interface] so the file
// can be directly imported into AmneziaWG / WireGuard clients.
func GenerateForExport(tunnel *storage.AWGTunnel) string {
	var b strings.Builder

	b.WriteString("[Interface]\n")
	b.WriteString(fmt.Sprintf("PrivateKey = %s\n", tunnel.Interface.PrivateKey))

	if tunnel.Interface.Address != "" {
		b.WriteString(fmt.Sprintf("Address = %s\n", tunnel.Interface.Address))
	}

	mtu := tunnel.Interface.MTU
	if mtu == 0 {
		mtu = DefaultMTU
	}
	b.WriteString(fmt.Sprintf("MTU = %d\n", mtu))

	if tunnel.Interface.DNS != "" {
		b.WriteString(fmt.Sprintf("DNS = %s\n", tunnel.Interface.DNS))
	}

	iface := &tunnel.Interface

	if iface.Jc > 0 {
		b.WriteString(fmt.Sprintf("Jc = %d\n", iface.Jc))
	}
	if iface.Jmin > 0 {
		b.WriteString(fmt.Sprintf("Jmin = %d\n", iface.Jmin))
	}
	if iface.Jmax > 0 {
		b.WriteString(fmt.Sprintf("Jmax = %d\n", iface.Jmax))
	}

	if iface.S1 > 0 {
		b.WriteString(fmt.Sprintf("S1 = %d\n", iface.S1))
	}
	if iface.S2 > 0 {
		b.WriteString(fmt.Sprintf("S2 = %d\n", iface.S2))
	}
	if iface.S3 > 0 {
		b.WriteString(fmt.Sprintf("S3 = %d\n", iface.S3))
	}
	if iface.S4 > 0 {
		b.WriteString(fmt.Sprintf("S4 = %d\n", iface.S4))
	}

	if iface.H1 != "" {
		b.WriteString(fmt.Sprintf("H1 = %s\n", iface.H1))
	}
	if iface.H2 != "" {
		b.WriteString(fmt.Sprintf("H2 = %s\n", iface.H2))
	}
	if iface.H3 != "" {
		b.WriteString(fmt.Sprintf("H3 = %s\n", iface.H3))
	}
	if iface.H4 != "" {
		b.WriteString(fmt.Sprintf("H4 = %s\n", iface.H4))
	}

	if iface.I1 != "" {
		b.WriteString(fmt.Sprintf("I1 = %s\n", iface.I1))
	}
	if iface.I2 != "" {
		b.WriteString(fmt.Sprintf("I2 = %s\n", iface.I2))
	}
	if iface.I3 != "" {
		b.WriteString(fmt.Sprintf("I3 = %s\n", iface.I3))
	}
	if iface.I4 != "" {
		b.WriteString(fmt.Sprintf("I4 = %s\n", iface.I4))
	}
	if iface.I5 != "" {
		b.WriteString(fmt.Sprintf("I5 = %s\n", iface.I5))
	}

	b.WriteString("\n[Peer]\n")
	b.WriteString(fmt.Sprintf("PublicKey = %s\n", tunnel.Peer.PublicKey))
	if tunnel.Peer.PresharedKey != "" {
		b.WriteString(fmt.Sprintf("PresharedKey = %s\n", tunnel.Peer.PresharedKey))
	}

	allowedIPs := tunnel.Peer.AllowedIPs
	if len(allowedIPs) == 0 {
		allowedIPs = DefaultAllowedIPs()
	}
	b.WriteString(fmt.Sprintf("AllowedIPs = %s\n", strings.Join(allowedIPs, ", ")))

	b.WriteString(fmt.Sprintf("Endpoint = %s\n", tunnel.Peer.Endpoint))

	keepalive := tunnel.Peer.PersistentKeepalive
	if keepalive == 0 {
		keepalive = DefaultPersistentKeepalive
	}
	b.WriteString(fmt.Sprintf("PersistentKeepalive = %d\n", keepalive))

	return b.String()
}

// GenerateForStart generates .conf content for tunnel start.
// When hasIPv6 is false, ::/0 is excluded from AllowedIPs to avoid
// broken IPv6 routing when the ISP doesn't provide IPv6.
func GenerateForStart(tunnel *storage.AWGTunnel, hasIPv6 bool) string {
	if hasIPv6 {
		return Generate(tunnel)
	}
	copy := *tunnel
	copy.Peer.AllowedIPs = filterIPv6DefaultRoute(tunnel.Peer.AllowedIPs)
	return Generate(&copy)
}

// filterIPv6DefaultRoute removes ::/0 from AllowedIPs.
func filterIPv6DefaultRoute(ips []string) []string {
	if len(ips) == 0 {
		return []string{"0.0.0.0/0"}
	}
	var result []string
	for _, ip := range ips {
		if strings.TrimSpace(ip) != "::/0" {
			result = append(result, ip)
		}
	}
	if len(result) == 0 {
		result = []string{"0.0.0.0/0"}
	}
	return result
}

// Parse parses WireGuard .conf content into an AWGTunnel.
func Parse(content string) (*storage.AWGTunnel, error) {
	tunnel := &storage.AWGTunnel{
		Type: "awg",
		Peer: storage.AWGPeer{
			PersistentKeepalive: DefaultPersistentKeepalive,
		},
	}

	var currentSection string
	var peerCount int

	scanner := bufio.NewScanner(strings.NewReader(content))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		lower := strings.ToLower(line)
		if lower == "[interface]" {
			currentSection = "interface"
			continue
		}
		if lower == "[peer]" {
			peerCount++
			if peerCount > 1 {
				return nil, ErrMultiplePeers
			}
			currentSection = "peer"
			continue
		}

		eqIndex := strings.Index(line, "=")
		if eqIndex == -1 {
			continue
		}

		key := strings.TrimSpace(line[:eqIndex])
		value := strings.TrimSpace(line[eqIndex+1:])
		keyLower := strings.ToLower(key)

		switch currentSection {
		case "interface":
			parseInterfaceField(tunnel, keyLower, value)
		case "peer":
			parsePeerField(tunnel, keyLower, value)
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	if tunnel.Interface.PrivateKey == "" {
		return nil, ErrMissingPrivateKey
	}
	if tunnel.Interface.Address == "" {
		return nil, ErrMissingAddress
	}
	if tunnel.Peer.PublicKey == "" {
		return nil, ErrMissingPublicKey
	}
	if tunnel.Peer.Endpoint == "" {
		return nil, ErrMissingEndpoint
	}

	if len(tunnel.Peer.AllowedIPs) == 0 {
		tunnel.Peer.AllowedIPs = DefaultAllowedIPs()
	}

	if tunnel.Interface.MTU == 0 {
		tunnel.Interface.MTU = DefaultMTU
	}

	return tunnel, nil
}

func parseInterfaceField(tunnel *storage.AWGTunnel, key, value string) {
	iface := &tunnel.Interface

	switch key {
	case "privatekey":
		iface.PrivateKey = value
	case "address":
		iface.Address = value
	case "dns":
		iface.DNS = value
	case "mtu":
		if v, err := strconv.Atoi(value); err == nil {
			iface.MTU = v
		}
	case "jc":
		if v, err := strconv.Atoi(value); err == nil {
			iface.Jc = v
		}
	case "jmin":
		if v, err := strconv.Atoi(value); err == nil {
			iface.Jmin = v
		}
	case "jmax":
		if v, err := strconv.Atoi(value); err == nil {
			iface.Jmax = v
		}
	case "s1":
		if v, err := strconv.Atoi(value); err == nil {
			iface.S1 = v
		}
	case "s2":
		if v, err := strconv.Atoi(value); err == nil {
			iface.S2 = v
		}
	case "s3":
		if v, err := strconv.Atoi(value); err == nil {
			iface.S3 = v
		}
	case "s4":
		if v, err := strconv.Atoi(value); err == nil {
			iface.S4 = v
		}
	case "h1":
		iface.H1 = value
	case "h2":
		iface.H2 = value
	case "h3":
		iface.H3 = value
	case "h4":
		iface.H4 = value
	case "i1":
		iface.I1 = value
	case "i2":
		iface.I2 = value
	case "i3":
		iface.I3 = value
	case "i4":
		iface.I4 = value
	case "i5":
		iface.I5 = value
	}
}

func parsePeerField(tunnel *storage.AWGTunnel, key, value string) {
	peer := &tunnel.Peer

	switch key {
	case "publickey":
		peer.PublicKey = value
	case "presharedkey":
		peer.PresharedKey = value
	case "endpoint":
		peer.Endpoint = value
	case "allowedips":
		parts := strings.Split(value, ",")
		for _, p := range parts {
			p = strings.TrimSpace(p)
			if p != "" {
				peer.AllowedIPs = append(peer.AllowedIPs, p)
			}
		}
	case "persistentkeepalive":
		if v, err := strconv.Atoi(value); err == nil {
			peer.PersistentKeepalive = v
		}
	}
}
