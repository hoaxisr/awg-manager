package router

// PolicyTunInboundSpec is the input to ensurePolicyTunInbound — everything the
// policy-tun mode's tun inbound needs that the caller already knows: the kernel
// tun device allocated for slot 20, its addresses, and the tun tunables.
type PolicyTunInboundSpec struct {
	Iface      string // kernel iface name, e.g. "opkgtun0"
	TunAddr4   string // e.g. "172.18.0.1/30"
	TunAddr6   string // e.g. "fdfe:dcba:9876::1/126" (empty to omit v6)
	MTU        int    //
	Stack      string // "gvisor" (default; empty → gvisor) or "system"
	UDPTimeout string // empty → DefaultUDPTimeout via resolveUDPTimeout
}

// ensurePolicyTunInbound replaces the tproxy/redirect inbound pair of slot 20
// with a single tun inbound. QoS inbounds (tproxy-qos-* / redirect-qos-*) are
// left untouched — they are bound per class and are not part of the main
// ingress. Re-running on its own output is a no-op (the old tun-in is dropped
// and rebuilt), so callers may apply it on every config write.
//
// Every auto-* flag is forced false: NDMS owns routing and redirect on this
// router, so sing-box must never touch the kernel routing table — traffic
// reaches the tun device because NDMS points a policy at it, not because
// sing-tun installed routes. The fields are *bool for the same reason as in
// ensureFakeIPOverlay: an explicit false must survive JSON marshaling,
// otherwise sing-box applies its own non-false defaults (auto_route true).
//
// No DNS or route rules are emitted here: slot 20's existing system rules
// (hijack-dns, udp_timeout) are inbound-agnostic and already cover tun ingress.
func ensurePolicyTunInbound(in []Inbound, spec PolicyTunInboundSpec) []Inbound {
	out := make([]Inbound, 0, len(in)+1)
	for _, i := range in {
		switch i.Tag {
		case "tun-in", "tproxy-in", "redirect-in":
			continue
		}
		out = append(out, i)
	}

	addrs := []string{spec.TunAddr4}
	if spec.TunAddr6 != "" {
		addrs = append(addrs, spec.TunAddr6)
	}
	// Stack: empty defaults to gvisor (robust, no gso flag). system REQUIRES
	// gso:false on this router's kernel (4.9) — the system stack with GSO panics
	// sing-tun under load (PoC-proven 2026-06-13).
	stack := spec.Stack
	if stack == "" {
		stack = "gvisor"
	}
	tun := Inbound{
		Type:                   "tun",
		Tag:                    "tun-in",
		InterfaceName:          spec.Iface,
		Address:                addrs,
		MTU:                    spec.MTU,
		AutoRoute:              boolPtr(false),
		AutoRedirect:           boolPtr(false),
		StrictRoute:            boolPtr(false),
		Stack:                  stack,
		EndpointIndependentNAT: boolPtr(false),
		UDPTimeout:             resolveUDPTimeout(spec.UDPTimeout),
	}
	if stack == "system" {
		tun.GSO = boolPtr(false)
	}
	return append([]Inbound{tun}, out...)
}

// filterPolicyTunInbound drops the policy-tun ingress from the inbound list —
// used when leaving policy-tun so the tun device is not reopened.
func filterPolicyTunInbound(in []Inbound) []Inbound {
	out := make([]Inbound, 0, len(in))
	for _, i := range in {
		if i.Tag == "tun-in" {
			continue
		}
		out = append(out, i)
	}
	return out
}
