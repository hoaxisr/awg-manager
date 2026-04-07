package staticroute

import (
	"fmt"
	"net"
	"sort"
	"strings"
)

// ParseBat parses a Windows .bat file containing static route commands.
// It extracts "route add <ip> mask <mask> <gateway>" lines, converts them
// to CIDR notation, deduplicates, and sorts the results.
// Non-fatal parse errors are collected and returned separately.
func ParseBat(content string) (subnets []string, parseErrors []string) {
	seen := make(map[string]struct{})

	for _, rawLine := range strings.Split(content, "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}

		lower := strings.ToLower(line)

		// Skip non-route-add lines (comments, echo, delete, etc.)
		if !strings.HasPrefix(lower, "route add") {
			continue
		}

		// Expected: route add <ip> mask <mask> <gateway> [metric ...]
		fields := strings.Fields(line)
		if len(fields) < 6 {
			parseErrors = append(parseErrors, fmt.Sprintf("too few fields: %s", line))
			continue
		}

		// fields[0]="route", fields[1]="add", fields[2]=ip, fields[3]="mask", fields[4]=mask, fields[5]=gateway
		if !strings.EqualFold(fields[3], "mask") {
			parseErrors = append(parseErrors, fmt.Sprintf("expected 'mask' keyword: %s", line))
			continue
		}

		ipStr := fields[2]
		maskStr := fields[4]

		ip := net.ParseIP(ipStr)
		if ip == nil {
			parseErrors = append(parseErrors, fmt.Sprintf("invalid IP %q: %s", ipStr, line))
			continue
		}

		prefix, ok := maskToCIDR(maskStr)
		if !ok {
			parseErrors = append(parseErrors, fmt.Sprintf("invalid or non-contiguous mask %q: %s", maskStr, line))
			continue
		}

		// Normalize via net.ParseCIDR (e.g., 10.1.2.3/8 -> 10.0.0.0/8)
		cidrStr := fmt.Sprintf("%s/%d", ip.String(), prefix)
		_, ipNet, err := net.ParseCIDR(cidrStr)
		if err != nil {
			parseErrors = append(parseErrors, fmt.Sprintf("CIDR parse error for %q: %s", cidrStr, line))
			continue
		}

		normalized := ipNet.String()
		if _, exists := seen[normalized]; exists {
			continue
		}
		seen[normalized] = struct{}{}
		comment := extractBatComment(fields[6:])
		subnets = append(subnets, FormatSubnetComment(normalized, comment))
	}

	sort.Strings(subnets)
	return subnets, parseErrors
}

// maskToCIDR converts a dotted-decimal subnet mask (e.g., "255.255.255.0")
// to a CIDR prefix length (e.g., 24). Returns (-1, false) if the mask is
// invalid or non-contiguous.
func maskToCIDR(mask string) (int, bool) {
	ip := net.ParseIP(mask)
	if ip == nil {
		return -1, false
	}

	ip4 := ip.To4()
	if ip4 == nil {
		return -1, false
	}

	// Combine into a single uint32 for contiguity check.
	bits := uint32(ip4[0])<<24 | uint32(ip4[1])<<16 | uint32(ip4[2])<<8 | uint32(ip4[3])

	// A valid mask is a sequence of 1-bits followed by 0-bits.
	// Invert: the inverted value plus one must be a power of two (or zero for /32).
	inverted := ^bits
	if inverted != 0 && (inverted&(inverted+1)) != 0 {
		return -1, false
	}

	// Count leading ones.
	count := 0
	for i := 31; i >= 0; i-- {
		if bits&(1<<uint(i)) != 0 {
			count++
		} else {
			break
		}
	}

	return count, true
}

// extractBatComment scans fields for a token starting with "!" and returns
// everything from "!" onwards joined as the comment string.
func extractBatComment(fields []string) string {
	for i, f := range fields {
		if strings.HasPrefix(f, "!") {
			// Join this and all subsequent fields as the comment
			combined := strings.Join(fields[i:], " ")
			return strings.TrimSpace(combined[1:]) // strip leading "!"
		}
	}
	return ""
}
