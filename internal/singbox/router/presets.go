package router

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

// PortRange represents a single port or a contiguous range of ports.
// When From == To it is a single port.
type PortRange struct {
	From int
	To   int
}

// String returns the iptables --dport representation: "N" for single ports,
// "N:M" for ranges (iptables multiport uses "N:M" range syntax).
func (pr PortRange) String() string {
	if pr.From == pr.To {
		return strconv.Itoa(pr.From)
	}
	return fmt.Sprintf("%d:%d", pr.From, pr.To)
}

// portRangeKey returns a canonical dedup key for a PortRange.
func portRangeKey(pr PortRange) string {
	return fmt.Sprintf("%d-%d", pr.From, pr.To)
}

type bypassPreset struct {
	UDP []int
	TCP []int
	// CIDRs — destination-IP исключения пресета (канонические IPv4 CIDR).
	// Вливаются в spec.BypassCIDRs наравне с bypassExtraSubnets, т.е. весь
	// трафик к этим адресам идёт мимо sing-box (включая DNS/53).
	CIDRs []string
}

// knownPresets maps preset name → UDP/TCP ports and destination CIDRs to
// exclude from TPROXY/REDIRECT.
var knownPresets = map[string]bypassPreset{
	"l2tp":        {UDP: []int{500, 4500, 1701}},
	"ntp":         {UDP: []int{123}},
	"netbios-smb": {UDP: []int{137, 138}, TCP: []int{139, 445}},
	// KeenDNS/CrazeDNS (#490, #729): статического CIDR у пресета нет —
	// адреса берутся с самого роутера (статические записи ndnproxy + адрес
	// доступа из /show/ndns) и приезжают в resolveBypassCIDRs отдельным
	// аргументом, см. syncKeenDNSPreset. Константы тут быть не может: с
	// прошивок 5.1.3/5.2 адрес сервиса переехал с 78.47.125.180 в
	// документационный 198.51.100.0/24 и отличается у Keenetic и NetCraze.
	// Запись нужна, чтобы имя оставалось валидным в BypassPresets.
	"keendns": {},
}

// PresetKeenDNS is the BypassPresets id for local KeenDNS/CrazeDNS rewrite.
const PresetKeenDNS = "keendns"

// resolveBypassPorts collects the final UDP and TCP port/range lists from
// named presets and the user-supplied extra-ports string.
// Returns an error if any preset name is unknown or the extra string is malformed.
func resolveBypassPorts(presets []string, extra string) (udp, tcp []PortRange, err error) {
	for _, name := range presets {
		ps, ok := knownPresets[name]
		if !ok {
			return nil, nil, fmt.Errorf("unknown bypass preset %q", name)
		}
		for _, p := range ps.UDP {
			udp = append(udp, PortRange{From: p, To: p})
		}
		for _, p := range ps.TCP {
			tcp = append(tcp, PortRange{From: p, To: p})
		}
	}
	eu, et, err := parseExtraPorts(extra)
	if err != nil {
		return nil, nil, err
	}
	udp = append(udp, eu...)
	tcp = append(tcp, et...)
	return udp, tcp, nil
}

// resolveBypassCIDRs собирает итоговый список destination-CIDR исключений:
// CIDR-часть именованных пресетов + рантайм-исключения (adhoc, сейчас это
// адрес KeenDNS с самого роутера) + пользовательский extra-список
// (resolveBypassSubnets). Дедуп со стабильным порядком — пользователь мог
// продублировать IP пресета вручную, а дубли в iptables-правилах не нужны.
func resolveBypassCIDRs(presets []string, extra string, adhoc []string) ([]string, error) {
	var out []string
	seen := make(map[string]struct{})
	add := func(c string) {
		if _, ok := seen[c]; ok {
			return
		}
		seen[c] = struct{}{}
		out = append(out, c)
	}
	for _, name := range presets {
		ps, ok := knownPresets[name]
		if !ok {
			return nil, fmt.Errorf("unknown bypass preset %q", name)
		}
		for _, c := range ps.CIDRs {
			add(c)
		}
	}
	for _, c := range adhoc {
		add(c)
	}
	subnets, err := resolveBypassSubnets(extra)
	if err != nil {
		return nil, err
	}
	for _, c := range subnets {
		add(c)
	}
	return out, nil
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

// parseExtraPorts parses a comma-separated list of port entries in the format
// "PORT UDP|TCP" or "PORT-PORT UDP|TCP" (range). Single ports and ranges are
// both supported. Empty string returns nil slices and no error.
// Case-insensitive for the protocol part.
//
// Examples:
//
//	"51820 UDP"         → UDP range {51820,51820}
//	"5000-5500 UDP"     → UDP range {5000,5500}
//	"51820 UDP, 443 TCP, 8000-9000 TCP"
func parseExtraPorts(s string) (udp, tcp []PortRange, err error) {
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
			return nil, nil, fmt.Errorf("invalid port entry %q: expected \"PORT UDP|TCP\" or \"PORT-PORT UDP|TCP\"", entry)
		}
		portPart := parts[0]
		var from, to int
		if idx := strings.Index(portPart, "-"); idx >= 0 {
			// Range: "5000-5500"
			fromStr := portPart[:idx]
			toStr := portPart[idx+1:]
			from, err = strconv.Atoi(fromStr)
			if err != nil || from < 1 || from > 65535 {
				return nil, nil, fmt.Errorf("invalid start port %q in %q: must be 1–65535", fromStr, entry)
			}
			to, err = strconv.Atoi(toStr)
			if err != nil || to < 1 || to > 65535 {
				return nil, nil, fmt.Errorf("invalid end port %q in %q: must be 1–65535", toStr, entry)
			}
			if from > to {
				return nil, nil, fmt.Errorf("invalid range %q in %q: start must be ≤ end", portPart, entry)
			}
		} else {
			// Single port
			from, err = strconv.Atoi(portPart)
			if err != nil || from < 1 || from > 65535 {
				return nil, nil, fmt.Errorf("invalid port %q in %q: must be 1–65535", portPart, entry)
			}
			to = from
		}
		pr := PortRange{From: from, To: to}
		switch strings.ToUpper(parts[1]) {
		case "UDP":
			udp = append(udp, pr)
		case "TCP":
			tcp = append(tcp, pr)
		default:
			return nil, nil, fmt.Errorf("invalid protocol %q in %q: must be UDP or TCP", parts[1], entry)
		}
	}
	return udp, tcp, nil
}
