package query

import (
	"context"
	"strings"
)

// GetInterfaceHotspotPolicy returns the ip hotspot policy bound to iface.
// "none" when the interface uses default-permit (no explicit policy line).
func (s *RunningConfigStore) GetInterfaceHotspotPolicy(ctx context.Context, iface string) (string, error) {
	lines, err := s.Lines(ctx)
	if err != nil {
		return "", err
	}
	ifaces, _ := HotspotPolicies(lines)
	if p, ok := ifaces[iface]; ok {
		return p, nil
	}
	return "none", nil
}

// HotspotPolicies — политики блока `ip hotspot`: интерфейсов и хостов (по MAC).
func HotspotPolicies(lines []string) (ifaces, hosts map[string]string) {
	ifaces, hosts = map[string]string{}, map[string]string{}
	inHotspot := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "ip hotspot" {
			inHotspot = true
			continue
		}
		if !inHotspot {
			continue
		}
		if trimmed == "!" {
			break
		}
		parts := strings.Fields(trimmed)
		switch {
		case len(parts) >= 3 && parts[0] == "policy": // policy <interface> <access|policyName>
			if _, seen := ifaces[parts[1]]; !seen {
				ifaces[parts[1]] = parts[2]
			}
		case len(parts) >= 4 && parts[0] == "host" && parts[2] == "policy": // host <mac> policy <policyName>
			hosts[strings.ToLower(parts[1])] = parts[3]
		}
	}
	return ifaces, hosts
}
