package sbconfig

import (
	"context"
	"fmt"
	"net"

	"github.com/hoaxisr/awg-manager/internal/uplink"
)

// MaterializeOwned runs at every tunnel-slot write, including ordinary edits.
// An endpoint change must not briefly re-enable unbound system DNS before the
// next controller tick. Removed targets are not recreated.
func MaterializeOwned(ctx context.Context, doc map[string]any, policies []uplink.Policy, providers []uplink.Provider, dns uplink.DNSForDevice) (map[string]any, error) {
	var specs []Spec
	obs, _ := doc["outbounds"].([]any)
	for _, p := range policies {
		if !p.Enabled || p.Target.Engine != "sing-box" || p.Target.Kind != "" {
			continue
		}
		for _, v := range obs {
			ob, _ := v.(map[string]any)
			if ob["tag"] != p.Target.Ref {
				continue
			}
			device, _ := ob["bind_interface"].(string)
			allowed := false
			for _, id := range p.Providers {
				for _, provider := range providers {
					if provider.ID == id && provider.Device == device && device != "" {
						allowed = true
					}
				}
			}
			if !allowed {
				return nil, fmt.Errorf("%w: managed target %q cannot drop or replace its WAN binding; disable its uplink policy first", uplink.ErrConflict, p.Target.Ref)
			}
			if err := ValidateProxyOptions(ob); err != nil {
				return nil, err
			}
			// A different managed target may currently be offline. Preserve its
			// already strict snapshot instead of blocking every unrelated edit
			// or another target's failover on the shared slot.
			if err := Verify(doc, p.Target.Ref, device, false); err == nil {
				continue
			}
			binding, err := uplink.InspectBinding(device)
			if err != nil {
				return nil, err
			}
			if server, _ := ob["server"].(string); net.ParseIP(server) == nil {
				if dns == nil {
					return nil, fmt.Errorf("explicit provider DNS is unavailable")
				}
				binding.DNSServers, err = dns(ctx, device)
				if err != nil {
					return nil, err
				}
			}
			specs = append(specs, Spec{Tag: p.Target.Ref, Binding: binding})
		}
	}
	return Apply(doc, specs)
}
