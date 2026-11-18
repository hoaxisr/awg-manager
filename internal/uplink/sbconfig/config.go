// Package sbconfig materializes strict WAN binding for sing-box configuration.
// It performs no network I/O and does not decide which WAN should be selected.
package sbconfig

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/netip"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/uplink"
)

const DNSPrefix = "awgm-uplink-dns-"

type Spec struct {
	Tag     string
	Binding uplink.Binding
	Group   bool
}

func DNSTag(tag string) string {
	sum := sha256.Sum256([]byte(tag))
	return DNSPrefix + hex.EncodeToString(sum[:16])
}

// Apply returns a deep copy, preserving domains, TLS/SNI/Host and unrelated DNS
// records. Every leaf in a managed group is bound, including inactive members.
// On any error the caller's document is untouched.
func Apply(doc map[string]any, specs []Spec) (map[string]any, error) {
	data, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err = json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	obs, _ := out["outbounds"].([]any)
	byTag := map[string]map[string]any{}
	for _, v := range obs {
		if ob, ok := v.(map[string]any); ok {
			tag, _ := ob["tag"].(string)
			if byTag[tag] != nil {
				return nil, fmt.Errorf("duplicate outbound %q", tag)
			}
			byTag[tag] = ob
		}
	}
	dns, _ := out["dns"].(map[string]any)
	if dns == nil {
		dns = map[string]any{}
	}
	servers, _ := dns["servers"].([]any)
	owners := map[string]string{}
	roots := map[string]bool{}
	for _, spec := range specs {
		if spec.Tag == "" || roots[spec.Tag] {
			return nil, fmt.Errorf("duplicate or empty managed target")
		}
		roots[spec.Tag] = true
		if err := spec.Binding.Validate(); err != nil {
			return nil, err
		}
		visiting := map[string]bool{}
		var visit func(string) error
		visit = func(tag string) error {
			if visiting[tag] {
				return fmt.Errorf("%w: cyclic outbound group", uplink.ErrUnsupported)
			}
			if owner, ok := owners[tag]; ok {
				if owner != spec.Tag {
					return fmt.Errorf("%w: shared managed outbound", uplink.ErrUnsupported)
				}
				return nil
			}
			ob := byTag[tag]
			if ob == nil {
				return fmt.Errorf("%w: missing outbound %q", uplink.ErrUnsupported, tag)
			}
			visiting[tag] = true
			defer delete(visiting, tag)
			typ, _ := ob["type"].(string)
			switch typ {
			case "selector", "urltest", "fallback", "load-balance", "loadbalance":
				if !spec.Group {
					return fmt.Errorf("%w: group target", uplink.ErrUnsupported)
				}
				members, _ := ob["outbounds"].([]any)
				if len(members) == 0 {
					return fmt.Errorf("%w: empty group", uplink.ErrUnsupported)
				}
				for _, v := range members {
					member, ok := v.(string)
					if !ok {
						return fmt.Errorf("invalid group member")
					}
					if err := visit(member); err != nil {
						return err
					}
				}
			default:
				server, err := configure(ob, spec.Binding)
				if err != nil {
					return err
				}
				if server != nil {
					replaced := false
					for i, v := range servers {
						existing, ok := v.(map[string]any)
						if ok && existing["tag"] == server["tag"] {
							servers[i] = server
							replaced = true
							break
						}
					}
					if !replaced {
						servers = append(servers, server)
					}
				}
			}
			owners[tag] = spec.Tag
			return nil
		}
		if err := visit(spec.Tag); err != nil {
			return nil, err
		}
	}
	// Prune only reserved generated records that have no remaining outbound
	// reference. This also covers domain->IP edits and removed members.
	used := map[string]bool{}
	for _, ob := range byTag {
		switch r := ob["domain_resolver"].(type) {
		case string:
			used[r] = true
		case map[string]any:
			s, _ := r["server"].(string)
			used[s] = true
		}
	}
	kept := make([]any, 0, len(servers))
	for _, v := range servers {
		s, ok := v.(map[string]any)
		if ok {
			tag, _ := s["tag"].(string)
			if strings.HasPrefix(tag, DNSPrefix) && !used[tag] {
				continue
			}
		}
		kept = append(kept, v)
	}
	if len(kept) > 0 || len(servers) > 0 {
		dns["servers"] = kept
		out["dns"] = dns
	}
	return out, nil
}

// ValidateProxyOptions rejects competing egress and unverified DNS bootstrap.
func ValidateProxyOptions(ob map[string]any) error {
	if ob["tcp_multi_path"] == true {
		return fmt.Errorf("%w: multipath TCP may use another WAN", uplink.ErrUnsupported)
	}
	for _, key := range []string{"network_strategy", "network_type", "fallback_network_type"} {
		if value := ob[key]; value != nil && value != "" {
			if list, ok := value.([]any); !ok || len(list) > 0 {
				return fmt.Errorf("%w: competing network selection %s", uplink.ErrUnsupported, key)
			}
		}
	}
	if tls, ok := ob["tls"].(map[string]any); ok {
		if ech, ok := tls["ech"].(map[string]any); ok && ech["enabled"] == true {
			return fmt.Errorf("%w: ECH DNS bootstrap is not verified for strict WAN policies", uplink.ErrUnsupported)
		}
	}
	switch ob["type"] {
	case "vless", "vmess", "trojan", "shadowsocks", "hysteria2", "tuic", "naive", "trusttunnel", "socks", "http", "anytls":
	default:
		return fmt.Errorf("%w: non-proxy group member", uplink.ErrUnsupported)
	}
	for _, key := range []string{"detour", "netns", "protect_path"} {
		if value := ob[key]; value != nil && value != "" {
			return fmt.Errorf("%w: conflicting dial field %s", uplink.ErrUnsupported, key)
		}
	}
	if value := ob["routing_mark"]; value != nil && value != float64(0) && value != "0" {
		return fmt.Errorf("%w: routing mark overrides WAN", uplink.ErrUnsupported)
	}
	server, _ := ob["server"].(string)
	if server == "" {
		return fmt.Errorf("%w: missing server", uplink.ErrUnsupported)
	}
	return nil
}

func configure(ob map[string]any, b uplink.Binding) (map[string]any, error) {
	if err := ValidateProxyOptions(ob); err != nil {
		return nil, err
	}
	server, _ := ob["server"].(string)
	ob["bind_interface"] = b.Device
	delete(ob, "inet4_bind_address")
	delete(ob, "inet6_bind_address")
	ip, ipErr := netip.ParseAddr(server)
	if ipErr == nil {
		ip = ip.Unmap()
		if ip.Is4() {
			if b.IPv4 == "" {
				return nil, fmt.Errorf("WAN has no IPv4 source")
			}
			ob["inet4_bind_address"] = b.IPv4
		} else {
			if b.IPv6 == "" {
				return nil, fmt.Errorf("WAN has no IPv6 source")
			}
			ob["inet6_bind_address"] = b.IPv6
		}
		if r, ok := ob["domain_resolver"].(map[string]any); ok {
			if r["server"] == DNSTag(stringValue(ob["tag"])) {
				delete(ob, "domain_resolver")
			}
		}
		return nil, nil
	}
	if b.IPv4 != "" {
		ob["inet4_bind_address"] = b.IPv4
	}
	if b.IPv6 != "" {
		ob["inet6_bind_address"] = b.IPv6
	}
	strategy := "prefer_ipv4"
	if b.IPv6 == "" {
		strategy = "ipv4_only"
	} else if b.IPv4 == "" {
		strategy = "ipv6_only"
	}
	var chosen netip.AddrPort
	for _, candidate := range b.DNSServers {
		a, err := uplink.ParseDNSServer(candidate)
		if err != nil {
			return nil, err
		}
		if (a.Addr().Is4() && b.IPv4 != "") || (!a.Addr().Is4() && b.IPv6 != "") {
			chosen = a
			break
		}
	}
	if !chosen.IsValid() {
		return nil, fmt.Errorf("WAN has no reachable explicit DNS server")
	}
	tag := DNSTag(stringValue(ob["tag"]))
	ob["domain_resolver"] = map[string]any{"server": tag, "strategy": strategy, "disable_cache": true}
	resolver := map[string]any{"type": "udp", "tag": tag, "server": chosen.Addr().String(), "server_port": chosen.Port(), "bind_interface": b.Device}
	if chosen.Addr().Is4() {
		resolver["inet4_bind_address"] = b.IPv4
	} else {
		resolver["inet6_bind_address"] = b.IPv6
	}
	return resolver, nil
}

func stringValue(v any) string { s, _ := v.(string); return s }
