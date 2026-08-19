// Package config provides WireGuard configuration parsing and generation.
package config

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// Default values for tunnel configuration.
const (
	DefaultMTU                 = 1280
	DefaultPersistentKeepalive = "25"
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

// writeAWGParams writes AWG obfuscation parameters to a .conf builder.
// If the tunnel is obfuscated, ALL base params are written including zero values.
// NDMS import rejects partial AWG configs (e.g. Jc present but S1 missing).
func writeAWGParams(b *strings.Builder, iface *storage.AWGInterface) {
	if !IsAWGObfuscated(iface) {
		return
	}
	b.WriteString(fmt.Sprintf("Jc = %d\n", iface.Jc))
	b.WriteString(fmt.Sprintf("Jmin = %d\n", iface.Jmin))
	b.WriteString(fmt.Sprintf("Jmax = %d\n", iface.Jmax))
	b.WriteString(fmt.Sprintf("S1 = %d\n", iface.S1))
	b.WriteString(fmt.Sprintf("S2 = %d\n", iface.S2))
	b.WriteString(fmt.Sprintf("H1 = %s\n", iface.H1))
	b.WriteString(fmt.Sprintf("H2 = %s\n", iface.H2))
	b.WriteString(fmt.Sprintf("H3 = %s\n", iface.H3))
	b.WriteString(fmt.Sprintf("H4 = %s\n", iface.H4))
	// Extended params (S3, S4, I1-I5) - only if any extended param is set
	if iface.S3 > 0 || iface.S4 > 0 || hasAnySignaturePacket(iface) {
		b.WriteString(fmt.Sprintf("S3 = %d\n", iface.S3))
		b.WriteString(fmt.Sprintf("S4 = %d\n", iface.S4))
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
	}
	writeAWG3Params(b, iface)
}

// writeAWG3Params emits AWG 3.0 device parameters (kernel module feat/awg3).
// Each is written only when set, so an AWG 1.x/2.x config stays byte-identical.
// Key names match the case-insensitive keys accepted by `awg setconf`.
func writeAWG3Params(b *strings.Builder, iface *storage.AWGInterface) {
	writeIfSet := func(key, val string) {
		if val != "" {
			b.WriteString(fmt.Sprintf("%s = %s\n", key, val))
		}
	}
	writeIfSet("HeaderProtectionKey", iface.HeaderProtectionKey)
	writeIfSet("ContentPaddingAddition", iface.ContentPaddingAddition)
	writeIfSet("RekeyAfterTime", iface.RekeyAfterTime)
	writeIfSet("RekeyTimeout", iface.RekeyTimeout)
	writeIfSet("RejectAfterTime", iface.RejectAfterTime)
	writeIfSet("KeepaliveTimeout", iface.KeepaliveTimeout)
	writeIfSet("MaxHandshakeAttempts", iface.MaxHandshakeAttempts)
	// `awg` reads these through parse_bool, which takes on/off (or a number)
	// and rejects "true" outright. Only the enabled state is written: off is
	// the device default, and emitting it would make a showconf round-trip of
	// a plain tunnel look like an awg3 config.
	if iface.RandomTrailers {
		b.WriteString("RandomTrailers = on\n")
	}
	if iface.DisableCookies {
		b.WriteString("DisableCookies = on\n")
	}
}

// Generate generates WireGuard .conf content from tunnel metadata.
func Generate(tunnel *storage.AWGTunnel) string {
	var b strings.Builder

	b.WriteString("[Interface]\n")
	b.WriteString(fmt.Sprintf("PrivateKey = %s\n", tunnel.Interface.PrivateKey))

	writeAWGParams(&b, &tunnel.Interface)

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
	if keepalive.IsZero() {
		keepalive = DefaultPersistentKeepalive
	}
	b.WriteString(fmt.Sprintf("PersistentKeepalive = %s\n", keepalive))

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

	writeAWGParams(&b, &tunnel.Interface)

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
	if keepalive.IsZero() {
		keepalive = DefaultPersistentKeepalive
	}
	b.WriteString(fmt.Sprintf("PersistentKeepalive = %s\n", keepalive))

	return b.String()
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
	// Нормализация формата: канонизирует небракетированный IPv6-с-портом
	// (встречается в выгрузках некоторых провайдеров) и отсекает мусорные
	// формы (без порта, нечисловой порт) с внятной ошибкой на импорте —
	// вместо загадочного отказа NDMS/резолвера при старте.
	normalized, err := NormalizeEndpoint(tunnel.Peer.Endpoint)
	if err != nil {
		return nil, fmt.Errorf("invalid Endpoint %q in [Peer]: %w", tunnel.Peer.Endpoint, err)
	}
	tunnel.Peer.Endpoint = normalized

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
	case "headerprotectionkey":
		iface.HeaderProtectionKey = value
	case "contentpaddingaddition":
		iface.ContentPaddingAddition = awg3Range(value)
	case "rekeyaftertime":
		iface.RekeyAfterTime = awg3Range(value)
	case "rekeytimeout":
		iface.RekeyTimeout = awg3Range(value)
	case "rejectaftertime":
		iface.RejectAfterTime = awg3Range(value)
	case "keepalivetimeout":
		iface.KeepaliveTimeout = awg3Range(value)
	case "maxhandshakeattempts":
		iface.MaxHandshakeAttempts = awg3Range(value)
	case "randomtrailers":
		iface.RandomTrailers = awg3Bool(value)
	case "disablecookies":
		iface.DisableCookies = awg3Bool(value)
	}
}

// awg3Bool разбирает булев ключ AWG 3.1 так же, как parse_bool в
// amneziawg-tools: "on" без учёта регистра либо ненулевое число. Всё
// остальное, включая "off" и пустую строку, — выключено.
//
// Отдельная функция, а не strconv.ParseBool: `awg showconf` печатает on/off,
// которых ParseBool не знает, и печатает их ВСЕГДА — ядро кладёт оба флага в
// дамп безусловно. Считать "off" отсутствием значения обязательно, иначе
// импорт showconf-вывода пометил бы обычный туннель как awg3. Та же ловушка,
// от которой защищает awg3Range.
func awg3Bool(value string) bool {
	v := strings.TrimSpace(value)
	if strings.EqualFold(v, "on") {
		return true
	}
	n, err := strconv.ParseUint(v, 10, 32)
	return err == nil && n != 0
}

// awg3Range нормализует значение AWG 3.0 диапазона из .conf. `awg showconf`
// печатает "0" для каждого незаданного параметра, поэтому импорт такого вывода
// превращал бы обычный AWG 2.0 туннель в awg3-подобный. Диапазон "0-80" при
// этом остаётся как есть — нулём считается только одиночный ноль.
func awg3Range(value string) string {
	if value == "0" {
		return ""
	}
	return value
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
		if ValidateKeepalive(storage.Keepalive(value)) == nil {
			peer.PersistentKeepalive = storage.Keepalive(value)
		}
	}
}

// NormalizeEndpoint приводит endpoint к канонической форме "host:port"
// (IPv6 — "[addr]:port"). Небракетированный IPv6-с-портом бракетируется;
// порт обязан быть числом 1-65535.
func NormalizeEndpoint(endpoint string) (string, error) {
	addr := strings.TrimSpace(endpoint)
	if host, port, err := net.SplitHostPort(addr); err == nil {
		if !validEndpointPort(port) {
			return "", fmt.Errorf("port %q must be a number 1-65535", port)
		}
		if strings.Contains(host, ":") {
			return net.JoinHostPort(host, port), nil
		}
		return host + ":" + port, nil
	}
	// Небракетированный IPv6:port — сплит по последнему двоеточию.
	if i := strings.LastIndex(addr, ":"); i > 0 {
		host, port := addr[:i], addr[i+1:]
		if ip := net.ParseIP(host); ip != nil && strings.Contains(host, ":") && validEndpointPort(port) {
			return net.JoinHostPort(host, port), nil
		}
	}
	return "", errors.New("expected host:port")
}

func validEndpointPort(p string) bool {
	n, err := strconv.Atoi(p)
	return err == nil && n >= 1 && n <= 65535
}
