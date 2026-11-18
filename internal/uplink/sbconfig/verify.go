package sbconfig

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/netip"

	"github.com/hoaxisr/awg-manager/internal/uplink"
)

// Verify checks actual persisted dial fields rather than trusting a subscription
// preference. It never treats an unbound DNS server or a group member on a
// different WAN as a valid probe path.
func Verify(doc map[string]any, tag, device string, group bool) error {
	obs, _ := doc["outbounds"].([]any)
	byTag := map[string]map[string]any{}
	for _, v := range obs {
		if ob, ok := v.(map[string]any); ok {
			t, _ := ob["tag"].(string)
			if byTag[t] != nil {
				return fmt.Errorf("duplicate outbound")
			}
			byTag[t] = ob
		}
	}
	dns, _ := doc["dns"].(map[string]any)
	servers, _ := dns["servers"].([]any)
	byDNS := map[string]map[string]any{}
	for _, v := range servers {
		if server, ok := v.(map[string]any); ok {
			t, _ := server["tag"].(string)
			if byDNS[t] != nil {
				return fmt.Errorf("duplicate DNS server")
			}
			byDNS[t] = server
		}
	}
	visiting := map[string]bool{}
	checked := map[string]bool{}
	var check func(string) error
	check = func(t string) error {
		if checked[t] {
			return nil
		}
		if visiting[t] {
			return fmt.Errorf("cyclic probe path")
		}
		visiting[t] = true
		defer func() { delete(visiting, t); checked[t] = true }()
		ob := byTag[t]
		if ob == nil {
			return fmt.Errorf("probe outbound missing")
		}
		switch ob["type"] {
		case "selector", "urltest", "fallback", "load-balance", "loadbalance":
			if !group {
				return uplink.ErrUnsupported
			}
			members, _ := ob["outbounds"].([]any)
			if len(members) == 0 {
				return fmt.Errorf("empty probe group")
			}
			for _, v := range members {
				member, ok := v.(string)
				if !ok {
					return fmt.Errorf("invalid probe member")
				}
				if err := check(member); err != nil {
					return err
				}
			}
			return nil
		}
		if ob["bind_interface"] != device {
			return fmt.Errorf("probe outbound binding differs from selected WAN")
		}
		binding := uplink.Binding{Device: device, IPv4: stringValue(ob["inet4_bind_address"]), IPv6: stringValue(ob["inet6_bind_address"])}
		var actualDNS map[string]any
		if _, err := netip.ParseAddr(stringValue(ob["server"])); err != nil {
			actualDNS = byDNS[DNSTag(t)]
			if actualDNS == nil {
				return fmt.Errorf("bound endpoint DNS is missing")
			}
			port := 53
			if value := actualDNS["server_port"]; value != nil {
				encoded, err := json.Marshal(value)
				if err != nil {
					return err
				}
				if err = json.Unmarshal(encoded, &port); err != nil {
					return fmt.Errorf("invalid endpoint DNS port")
				}
			}
			ip, err := netip.ParseAddr(stringValue(actualDNS["server"]))
			if err != nil || port < 1 || port > 65535 {
				return fmt.Errorf("invalid endpoint DNS")
			}
			binding.DNSServers = []string{netip.AddrPortFrom(ip, uint16(port)).String()}
		}
		if err := binding.Validate(); err != nil {
			return err
		}
		data, _ := json.Marshal(ob)
		var expected map[string]any
		json.Unmarshal(data, &expected)
		dnsServer, err := configure(expected, binding)
		if err != nil {
			return err
		}
		for _, key := range []string{"bind_interface", "inet4_bind_address", "inet6_bind_address", "domain_resolver"} {
			if !sameJSON(ob[key], expected[key]) {
				return fmt.Errorf("probe dial field %s changed", key)
			}
		}
		if !sameJSON(actualDNS, dnsServer) {
			return fmt.Errorf("endpoint DNS is not strictly bound to the selected WAN")
		}
		return nil
	}
	return check(tag)
}

func sameJSON(a, b any) bool {
	x, err := json.Marshal(a)
	if err != nil {
		return false
	}
	y, err := json.Marshal(b)
	return err == nil && bytes.Equal(x, y)
}
