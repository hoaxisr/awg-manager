package api

import "github.com/hoaxisr/awg-manager/internal/events"

// Resource keys — closed set. Keep in sync with
// frontend/src/lib/stores/storeRegistry.ts.
const (
	ResourceTunnels                 = "tunnels"
	ResourceServers                 = "servers"
	ResourceSingboxStatus           = "singbox.status"
	ResourceSingboxTunnels          = "singbox.tunnels"
	ResourceSysInfo                 = "sysInfo"
	ResourcePingcheck               = "pingcheck"
	ResourceSaveStatus              = "saveStatus"
	ResourceRoutingDnsRoutes        = "routing.dnsRoutes"
	ResourceRoutingStaticRoutes     = "routing.staticRoutes"
	ResourceRoutingAccessPolicies   = "routing.accessPolicies"
	ResourceRoutingPolicyDevices    = "routing.policyDevices"
	ResourceRoutingPolicyInterfaces = "routing.policyInterfaces"
	ResourceRoutingClientRoutes     = "routing.clientRoutes"
	ResourceRoutingTunnels          = "routing.tunnels"
)

// publishInvalidated posts a resource:invalidated hint to the SSE bus.
// Safe when bus is nil (e.g. in tests that construct a handler without
// a bus).
func publishInvalidated(bus *events.Bus, resource, reason string) {
	if bus == nil {
		return
	}
	bus.Publish("resource:invalidated", events.ResourceInvalidatedEvent{
		Resource: resource,
		Reason:   reason,
	})
}
