package router

import (
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
)

type portSet struct {
	UDP []int
	TCP []int
}

// knownPresets maps preset name → UDP and TCP ports to exclude from TPROXY/REDIRECT.
var knownPresets = map[string]portSet{
	"l2tp":        {UDP: []int{500, 4500, 1701}},
	"ntp":         {UDP: []int{123}},
	"netbios-smb": {UDP: []int{137, 138}, TCP: []int{139, 445}},
}

// KnownPresetNames returns the sorted list of valid preset names.
// Used by ValidateSingboxRouterSettings.
func KnownPresetNames() []string {
	names := make([]string, 0, len(knownPresets))
	for k := range knownPresets {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// resolveBypassPorts collects the final UDP and TCP port lists from named
// presets and the user-supplied extra-ports string.
// Returns an error if any preset name is unknown or the extra string is malformed.
func resolveBypassPorts(presets []string, extra string) (udp, tcp []int, err error) {
	for _, name := range presets {
		ps, ok := knownPresets[name]
		if !ok {
			return nil, nil, fmt.Errorf("unknown bypass preset %q", name)
		}
		udp = append(udp, ps.UDP...)
		tcp = append(tcp, ps.TCP...)
	}
	eu, et, err := parseExtraPorts(extra)
	if err != nil {
		return nil, nil, err
	}
	udp = append(udp, eu...)
	tcp = append(tcp, et...)
	return udp, tcp, nil
}

// resolveBypassSubnets парсит список IPv4 IP/CIDR (через запятую/пробел) в
// канонизированные CIDR. Голый IP → "/32". Hostname, IPv6 и мусор отвергаются —
// иначе невалидный "-d" уронит весь iptables-restore COMMIT. Пустая строка →
// (nil, nil).
func resolveBypassSubnets(extra string) ([]string, error) {
	extra = strings.TrimSpace(extra)
	if extra == "" {
		return nil, nil
	}
	var out []string
	for _, field := range strings.FieldsFunc(extra, func(r rune) bool { return r == ',' || r == ' ' }) {
		s := strings.TrimSpace(field)
		if s == "" {
			continue
		}
		if _, ipnet, err := net.ParseCIDR(s); err == nil {
			if ipnet.IP.To4() == nil {
				return nil, fmt.Errorf("IPv6 не поддерживается: %q", s)
			}
			out = append(out, ipnet.String())
			continue
		}
		if ip := net.ParseIP(s); ip != nil && ip.To4() != nil {
			out = append(out, ip.String()+"/32")
			continue
		}
		return nil, fmt.Errorf("неверный IP/CIDR: %q", s)
	}
	return out, nil
}

// parseExtraPorts parses a comma-separated list of "<port> <UDP|TCP>" entries.
// Empty string returns nil slices and no error.
// Case-insensitive for the protocol part.
func parseExtraPorts(s string) (udp, tcp []int, err error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil, nil
	}
	for _, entry := range strings.Split(s, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		parts := strings.Fields(entry)
		if len(parts) != 2 {
			return nil, nil, fmt.Errorf("invalid port entry %q: expected \"PORT UDP|TCP\"", entry)
		}
		port, err := strconv.Atoi(parts[0])
		if err != nil || port < 1 || port > 65535 {
			return nil, nil, fmt.Errorf("invalid port %q in %q: must be 1–65535", parts[0], entry)
		}
		switch strings.ToUpper(parts[1]) {
		case "UDP":
			udp = append(udp, port)
		case "TCP":
			tcp = append(tcp, port)
		default:
			return nil, nil, fmt.Errorf("invalid protocol %q in %q: must be UDP or TCP", parts[1], entry)
		}
	}
	return udp, tcp, nil
}
