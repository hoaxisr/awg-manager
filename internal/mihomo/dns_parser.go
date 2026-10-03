package mihomo

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

// isValidLinuxInterfaceName checks if s conforms to standard Linux network interface naming
// rules (up to 15 characters, alphanumeric, underscores, hyphens, and dots).
func isValidLinuxInterfaceName(s string) bool {
	if len(s) == 0 || len(s) > 15 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '-' || c == '.' {
			continue
		}
		return false
	}
	return true
}

// isValidParamKey checks if a query parameter key contains only alphanumeric, hyphen, and underscore characters.
func isValidParamKey(s string) bool {
	if len(s) == 0 || len(s) > 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '-' {
			continue
		}
		return false
	}
	return true
}

// NormalizeAndValidateDNSServerURI parses, validates, and normalizes a Mihomo DNS server entry.
//
// Syntax supported per Mihomo specification:
//   - Plain IPs: 8.8.8.8, 1.1.1.1:53, [2001:4860:4860::8888]:53
//   - Special resolvers: system, hosts
//   - URI schemes: udp://, tcp://, tls://, https://, quic://, dhcp://, rcode://
//   - Fragments (#...):
//   - Positional route selector: RULES (normalized to uppercase), known proxy/group, or valid Linux interface name
//   - Query parameters (&...): ecs=..., ecs-override=..., h3=..., skip-cert-verify=..., name-cert-verify=...,
//     disable-ipv4=..., disable-ipv6=..., disable-qtype-<int>=..., or well-formed forward-compatible parameters
//
// isValidDomainName validates standard DNS domain names.
func isValidDomainName(s string) bool {
	if len(s) == 0 || len(s) > 253 {
		return false
	}
	labels := strings.Split(s, ".")
	for _, l := range labels {
		if len(l) == 0 || len(l) > 63 {
			return false
		}
		if l[0] == '-' || l[len(l)-1] == '-' {
			return false
		}
		for i := 0; i < len(l); i++ {
			c := l[i]
			if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_' {
				continue
			}
			return false
		}
	}
	return true
}

// validateDNSBase validates and normalizes the base part of a DNS endpoint URI (pre-fragment).
func validateDNSBase(base string) (string, error) {
	if strings.ContainsAny(base, " \t\r\n") {
		return "", CompileErrorf("dns", base, "", "DNS server base %q contains whitespace", base)
	}
	for i := 0; i < len(base); i++ {
		if base[i] < 32 || base[i] == 127 {
			return "", CompileErrorf("dns", base, "", "DNS server base %q contains control characters", base)
		}
	}

	lower := strings.ToLower(base)
	if lower == "system" || lower == "hosts" || lower == "system://" {
		return lower, nil
	}

	if strings.Contains(base, "://") {
		scheme, rest, _ := strings.Cut(base, "://")
		scheme = strings.ToLower(scheme)
		switch scheme {
		case "system":
			if rest != "" {
				return "", CompileErrorf("dns", base, "", "system:// scheme does not accept host, port, or path: %q", base)
			}
			return "system://", nil
		case "dhcp":
			rest = strings.TrimSpace(rest)
			if rest == "" {
				return "", CompileErrorf("dns", base, "", "dhcp:// scheme requires interface name or 'system'")
			}
			if strings.ContainsAny(rest, "/:?#@") {
				return "", CompileErrorf("dns", base, "", "dhcp:// scheme does not accept path or port: %q", base)
			}
			if strings.EqualFold(rest, "system") {
				return "dhcp://system", nil
			}
			if !isValidLinuxInterfaceName(rest) {
				return "", CompileErrorf("dns", base, "", "invalid interface name %q in dhcp:// scheme", rest)
			}
			return fmt.Sprintf("dhcp://%s", rest), nil
		case "rcode":
			code := strings.ToLower(strings.TrimSpace(rest))
			validRcodes := map[string]bool{
				"success":         true,
				"format_error":    true,
				"server_failure":  true,
				"name_error":      true,
				"not_implemented": true,
				"refused":         true,
			}
			if !validRcodes[code] {
				return "", CompileErrorf("dns", base, "", "unsupported rcode %q in %q", rest, base)
			}
			return fmt.Sprintf("rcode://%s", code), nil
		case "udp", "tcp", "tls", "https", "quic":
			if strings.Contains(rest, "@") {
				return "", CompileErrorf("dns", base, "", "DNS server URI %q cannot contain user credentials", base)
			}
			hostPart, pathPart, hasPath := strings.Cut(rest, "/")
			if hasPath && scheme != "https" {
				return "", CompileErrorf("dns", base, "", "scheme %s:// does not support URL path %q", scheme, "/"+pathPart)
			}
			if hostPart == "" {
				return "", CompileErrorf("dns", base, "", "DNS server URI %q has empty host", base)
			}
			var host, portStr string
			if strings.HasPrefix(hostPart, "[") {
				closeIdx := strings.Index(hostPart, "]")
				if closeIdx == -1 {
					return "", CompileErrorf("dns", base, "", "unclosed IPv6 bracket in %q", base)
				}
				host = hostPart[1:closeIdx]
				restHost := hostPart[closeIdx+1:]
				if strings.HasPrefix(restHost, ":") {
					portStr = restHost[1:]
					if portStr == "" {
						return "", CompileErrorf("dns", base, "", "invalid empty port after colon in %q", base)
					}
				} else if restHost != "" {
					return "", CompileErrorf("dns", base, "", "invalid characters after IPv6 bracket in %q", base)
				}
			} else if strings.Contains(hostPart, ":") {
				h, p, err := net.SplitHostPort(hostPart)
				if err != nil || p == "" {
					return "", CompileErrorf("dns", base, "", "invalid host:port %q", hostPart)
				}
				host, portStr = h, p
			} else {
				host = hostPart
			}

			if host == "" {
				return "", CompileErrorf("dns", base, "", "empty host in DNS server URI %q", base)
			}
			if ip := net.ParseIP(host); ip == nil && !isValidDomainName(host) {
				return "", CompileErrorf("dns", base, "", "invalid hostname or IP %q in %q", host, base)
			}
			if portStr != "" {
				portNum, err := strconv.Atoi(portStr)
				if err != nil || portNum < 1 || portNum > 65535 {
					return "", CompileErrorf("dns", base, "", "invalid port %q in %q", portStr, base)
				}
			}
			if hasPath {
				return fmt.Sprintf("%s://%s/%s", scheme, hostPart, pathPart), nil
			}
			return fmt.Sprintf("%s://%s", scheme, hostPart), nil
		default:
			return "", CompileErrorf("dns", base, "", "unsupported DNS scheme %q in %q", scheme, base)
		}
	}

	// Plain address or host[:port]
	var host, portStr string
	if strings.HasPrefix(base, "[") {
		closeIdx := strings.Index(base, "]")
		if closeIdx == -1 {
			return "", CompileErrorf("dns", base, "", "unclosed IPv6 bracket in %q", base)
		}
		host = base[1:closeIdx]
		rest := base[closeIdx+1:]
		if strings.HasPrefix(rest, ":") {
			portStr = rest[1:]
			if portStr == "" {
				return "", CompileErrorf("dns", base, "", "invalid empty port after colon in %q", base)
			}
		} else if rest != "" {
			return "", CompileErrorf("dns", base, "", "invalid characters after IPv6 bracket in %q", base)
		}
	} else if ip := net.ParseIP(base); ip != nil {
		return base, nil
	} else if strings.Contains(base, ":") {
		h, p, err := net.SplitHostPort(base)
		if err != nil || p == "" {
			return "", CompileErrorf("dns", base, "", "invalid address %q", base)
		}
		host, portStr = h, p
	} else {
		host = base
	}

	if ip := net.ParseIP(host); ip == nil && !isValidDomainName(host) {
		return "", CompileErrorf("dns", base, "", "invalid DNS server address %q", base)
	}
	if portStr != "" {
		portNum, err := strconv.Atoi(portStr)
		if err != nil || portNum < 1 || portNum > 65535 {
			return "", CompileErrorf("dns", base, "", "invalid port %q in %q", portStr, base)
		}
	}
	return base, nil
}

// NormalizeAndValidateDNSServerURI parses, validates, and normalizes a Mihomo DNS server entry.
func NormalizeAndValidateDNSServerURI(entry string, validTarget func(string) bool) (string, error) {
	raw := strings.TrimSpace(entry)
	if raw == "" {
		return "", CompileErrorf("dns", "", "", "DNS server URI cannot be empty")
	}

	if strings.Count(raw, "#") > 1 {
		return "", CompileErrorf("dns", raw, "", "multiple '#' fragments in DNS server entry %q", raw)
	}

	hashIdx := strings.Index(raw, "#")
	if hashIdx == -1 {
		// Plain address or URI without fragment
		return validateDNSBase(raw)
	}

	base := strings.TrimSpace(raw[:hashIdx])
	if base == "" {
		return "", CompileErrorf("dns", raw, "", "DNS server entry %q has empty base address before '#'", raw)
	}
	normBase, err := validateDNSBase(base)
	if err != nil {
		return "", err
	}

	frag := strings.TrimSpace(raw[hashIdx+1:])
	if frag == "" {
		return "", CompileErrorf("dns", raw, "", "DNS server entry %q has empty fragment after '#'", raw)
	}

	parts := strings.Split(frag, "&")
	var normParts []string

	for i, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			return "", CompileErrorf("dns", raw, "", "DNS server entry %q contains empty fragment parameter", raw)
		}

		if strings.Contains(p, "=") {
			// Parameter key=value
			key, val, _ := strings.Cut(p, "=")
			key = strings.TrimSpace(key)
			val = strings.TrimSpace(val)
			if key == "" || val == "" {
				return "", CompileErrorf("dns", p, raw, "DNS fragment parameter %q must have non-empty key and value", p)
			}
			if !isValidParamKey(key) {
				return "", CompileErrorf("dns", key, raw, "DNS fragment parameter key %q contains invalid characters", key)
			}
			// Validate that value does not contain invalid control characters or spaces
			for j := 0; j < len(val); j++ {
				if val[j] <= ' ' || val[j] == 0x7f {
					return "", CompileErrorf("dns", val, raw, "DNS fragment parameter value %q contains invalid characters", val)
				}
			}
			normParts = append(normParts, fmt.Sprintf("%s=%s", key, val))
		} else {
			// Positional route selector: must be the very first fragment token
			if i != 0 {
				return "", CompileErrorf("dns", p, raw, "DNS fragment selector %q must precede query parameters", p)
			}

			if strings.EqualFold(p, "RULES") {
				normParts = append(normParts, "RULES")
			} else if validTarget != nil && validTarget(p) {
				normParts = append(normParts, p)
			} else if isValidLinuxInterfaceName(p) {
				normParts = append(normParts, p)
			} else {
				return "", CompileErrorf("dns", p, raw, "DNS server fragment references unknown target or interface %q", p)
			}
		}
	}

	normalized := fmt.Sprintf("%s#%s", normBase, strings.Join(normParts, "&"))
	return normalized, nil
}
