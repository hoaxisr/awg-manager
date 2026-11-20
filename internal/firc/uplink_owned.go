package firc

import (
	"context"
	"fmt"

	"github.com/hoaxisr/awg-manager/internal/uplink"
)

func MaterializeOwnedUplinks(ctx context.Context, tunnels []Tunnel, policies []uplink.Policy, providers []uplink.Provider, dns uplink.DNSForDevice, client *Client) ([]Tunnel, error) {
	result := append([]Tunnel(nil), tunnels...)
	dnsChecked := false
	for _, p := range policies {
		if !p.Enabled || p.Target.Engine != "firc" || p.Target.Kind != "" {
			continue
		}
		for i := range result {
			t := &result[i]
			if t.ID != p.Target.Ref || !t.Enable {
				continue
			}
			allowed := false
			if t.Uplink.Kind == "iface" {
				for _, id := range p.Providers {
					for _, provider := range providers {
						if provider.ID == id && provider.Device == t.Uplink.Ref && provider.Device != "" {
							allowed = true
						}
					}
				}
			}
			if !allowed {
				return nil, fmt.Errorf("%w: managed FIRC target %q requires an allowed explicit WAN; disable its policy first", uplink.ErrConflict, t.ID)
			}
			if requiresUplinkDNS(*t) {
				if dns == nil || client == nil {
					return nil, uplink.ErrUnsupported
				}
				if !dnsChecked {
					health, err := client.Ping(ctx)
					if err != nil {
						return nil, err
					}
					if health.UplinkDNSVersion < 1 {
						return nil, fmt.Errorf("%w: FIRC WAN DNS capability required", uplink.ErrUnsupported)
					}
					dnsChecked = true
				}
				servers, err := dns(ctx, t.Uplink.Ref)
				if err != nil {
					return nil, err
				}
				if err = validateEndpointDNS(servers); err != nil {
					return nil, err
				}
				t.EndpointDNS = append([]string(nil), servers...)
			}
		}
	}
	return result, nil
}
