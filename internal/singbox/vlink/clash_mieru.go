package vlink

import (
	"errors"
	"fmt"

	pb "github.com/enfein/mieru/v3/pkg/appctl/appctlpb"
)

// mapClashMieru converts a Clash/mihomo "type: mieru" proxy into a
// sing-box Mieru outbound. Clash allows either port or port-range, not both.
func mapClashMieru(p map[string]any) (*ParsedOutbound, error) {
	host := asString(p["server"])
	if host == "" {
		return nil, errors.New("clash mieru: missing server")
	}
	username := asString(p["username"])
	if username == "" {
		return nil, errors.New("clash mieru: missing username")
	}
	password := asString(p["password"])
	if password == "" {
		return nil, errors.New("clash mieru: missing password")
	}
	transport := asString(p["transport"])
	switch transport {
	case "TCP", "UDP":
	default:
		return nil, fmt.Errorf("clash mieru: invalid transport %q", transport)
	}

	_, hasPort := p["port"]
	_, hasRange := p["port-range"]
	if hasPort == hasRange {
		return nil, errors.New("clash mieru: set exactly one of port or port-range")
	}

	var specs []mieruPortSpec
	if hasPort {
		portN, ok := asInt(p["port"])
		if !ok || portN < 1 || portN > 65535 {
			return nil, errors.New("clash mieru: missing or invalid port")
		}
		specs = append(specs, mieruPortSpec{Value: fmt.Sprintf("%d", portN), Numeric: true})
	} else {
		rng, err := normalizeMieruPortRange(asString(p["port-range"]))
		if err != nil {
			return nil, fmt.Errorf("clash mieru: %w", err)
		}
		specs = append(specs, mieruPortSpec{Value: rng})
	}

	params := mieruParams{
		host:           host,
		username:       username,
		password:       password,
		transport:      transport,
		specs:          specs,
		trafficPattern: asString(p["traffic-pattern"]),
		label:          asString(p["name"]),
	}
	if mux := asString(p["multiplexing"]); mux != "" {
		if _, ok := pb.MultiplexingLevel_value[mux]; !ok {
			return nil, fmt.Errorf("clash mieru: invalid multiplexing %q", mux)
		}
		params.multiplexing = mux
	}
	return assembleMieruOutbound(params)
}
