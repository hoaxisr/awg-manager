package router

import "errors"

var (
	ErrNetfilterComponentMissing = errors.New("kernel module xt_TPROXY.ko not found: install the router firmware 'Netfilter kernel modules' component")
	ErrIPTablesModTProxyMissing  = errors.New("iptables-mod-tproxy package not installed")
	ErrRuleSetReferenced         = errors.New("rule set is referenced by one or more rules")
	ErrOutboundReferenced        = errors.New("outbound is referenced by one or more rules")
	ErrInvalidMatchers           = errors.New("rule must have at least one matcher")
	ErrRuleIndexOutOfRange       = errors.New("rule index out of range")
	ErrRuleSetTagConflict        = errors.New("rule set with this tag already exists")
	ErrRuleSetNotFound           = errors.New("rule set not found")
	ErrDatRuleSetForbidden       = errors.New("dat rule set token is invalid")
	ErrOutboundTagConflict       = errors.New("outbound with this tag already exists")
	ErrDNSServerTagConflict      = errors.New("dns server with this tag already exists")
	ErrDNSServerReferenced       = errors.New("dns server is referenced by one or more dns rules or used as final/default")
	ErrDNSServerNotFound         = errors.New("dns server not found")
	ErrDNSRuleIndexOutOfRange    = errors.New("dns rule index out of range")
	ErrDNSInvalidServer          = errors.New("dns rule references unknown server tag")

	ErrPolicyNotConfigured = errors.New("router policy not configured (settings.policyName is empty)")
	ErrPolicyMissing       = errors.New("policy has no fwmark in NDMS (deleted or has no permitted interface)")

	// ErrSingboxNotReady is returned by Enable when sing-box did not
	// become ready within the boot-wait window. Callers should surface
	// this as 503 Service Unavailable — the iptables/policy install was
	// deliberately skipped to avoid orphaning DNS:53 redirects at a
	// torn-down sing-box port (issue #221).
	ErrSingboxNotReady = errors.New("sing-box did not become ready within boot-wait window — iptables install skipped")
)
