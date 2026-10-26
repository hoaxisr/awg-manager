package mcp

import "context"

// Deps is everything the tools need from the host. Production wires
// localdeps (internal services); tests and cmd/mcp-dev wire mcptest.Fake.
// Implementations return plain errors with user-facing messages: tools
// forward err.Error() to the model verbatim.
type Deps interface {
	SystemStatus(ctx context.Context) (SystemStatus, error)

	ListTunnels(ctx context.Context) ([]TunnelSummary, error)
	GetTunnel(ctx context.Context, id string) (TunnelDetail, error)
	ControlTunnel(ctx context.Context, id, action string) error
	// ImportTunnel creates a tunnel from .conf text. warnings carries
	// address conflicts with other interfaces (non-fatal, as in the REST
	// import): the tunnel exists but may not route until the user resolves
	// them, and the model must not enable it as if all were well.
	ImportTunnel(ctx context.Context, name, config string) (created TunnelSummary, warnings []string, err error)
	// ReplaceTunnelConfig swaps a tunnel's .conf. A RUNNING tunnel is
	// stopped and started around the swap (a kernel tunnel does not pick up
	// a new Address/DNS/MTU otherwise), so the returned warnings carry both
	// a failed restart and any address conflicts the new config introduces.
	ReplaceTunnelConfig(ctx context.Context, id, config, newName string) (warnings []string, err error)
	ExportTunnelConfig(ctx context.Context, id string) (string, error)
	// UpdateTunnelSettings changes individual settings the way the web
	// interface's tunnel card does — same checks, a running tunnel applied
	// live or restarted when the WAN connection changes — and returns the
	// tunnel afterwards with any address conflicts the edit introduced.
	UpdateTunnelSettings(ctx context.Context, in TunnelSettingsInput) (TunnelDetail, []string, error)

	ListDNSRoutes(ctx context.Context) ([]DNSRoute, error)
	// GetDNSRoute returns one list in full — Domains uncapped, plus the
	// excludes and subscriptions the list view drops. The tool pages
	// Domains; implementations must not truncate them here.
	GetDNSRoute(ctx context.Context, id string) (DNSRouteDetail, error)
	// ListDNSRouteDetails returns every list in full in one call, for
	// tools that must match against whole lists (explain_route). One call
	// because per-id reads of HydraRoute lists re-read its config files
	// each time.
	ListDNSRouteDetails(ctx context.Context) ([]DNSRouteDetail, error)
	AddDNSRoute(ctx context.Context, in DNSRouteInput) (DNSRoute, error)
	// UpdateDNSRoute applies a partial edit and returns the list as it
	// stands afterwards. warnings carries losses the edit caused that the
	// caller did not ask for — replacing a multi-target list's routes with
	// the single tunnel the input names.
	UpdateDNSRoute(ctx context.Context, in DNSRouteUpdate) (updated DNSRoute, warnings []string, err error)
	// SetDNSRouteEnabled toggles a list on or off and returns it as it
	// stands afterwards. Reversible: the record survives either way.
	SetDNSRouteEnabled(ctx context.Context, id string, enabled bool) (DNSRoute, error)
	// RemoveDNSRoute deletes the list and returns it as it was; the
	// deletion is permanent, so the record is the only thing left to show
	// the user. See tools_routing.go removedDNSOut.
	RemoveDNSRoute(ctx context.Context, id string) (DNSRoute, error)

	// ListRoutingTargets returns what a routing list can send traffic
	// through, as the web interface's target picker lists it, without the
	// WireGuard servers.
	ListRoutingTargets(ctx context.Context) ([]RoutingTarget, error)

	ListStaticRoutes(ctx context.Context) ([]StaticRoute, error)
	// UpdateStaticRoute applies a partial edit and returns the list as it
	// stands afterwards; the kill switch, the icon and the on/off state
	// survive an edit that does not name them.
	UpdateStaticRoute(ctx context.Context, in StaticRouteUpdate) (StaticRoute, error)
	AddStaticRoute(ctx context.Context, in StaticRouteInput) (StaticRoute, error)
	// SetStaticRouteEnabled is SetDNSRouteEnabled for subnet lists.
	SetStaticRouteEnabled(ctx context.Context, id string, enabled bool) (StaticRoute, error)
	// RemoveStaticRoute deletes the list and returns it as it was.
	RemoveStaticRoute(ctx context.Context, id string) (StaticRoute, error)

	ListClientRoutes(ctx context.Context) ([]ClientRoute, error)
	SetClientRoute(ctx context.Context, in ClientRouteInput) (*ClientRoute, error) // nil when removed
	// SetClientRouteEnabled switches one device's route without removing
	// it. Addressed by client IP, as SetClientRoute is: that is what the
	// device list gives an agent. The IP is already canonical here.
	SetClientRouteEnabled(ctx context.Context, clientIP string, enabled bool) (ClientRoute, error)

	ListAccessPolicies(ctx context.Context) ([]AccessPolicy, error)
	// ListPolicyInterfaces returns the router interfaces a policy can
	// permit.
	ListPolicyInterfaces(ctx context.Context) ([]PolicyInterface, error)
	// The access policy writes below apply at once in NDMS. HydraRoute's
	// own policies are refused by the service.

	// CreateAccessPolicy creates a policy in the first free PolicyN slot.
	CreateAccessPolicy(ctx context.Context, description string) (AccessPolicy, error)
	// DeleteAccessPolicy removes a policy and returns it as it was; the
	// policy the sing-box router routes by is refused.
	DeleteAccessPolicy(ctx context.Context, name string) (AccessPolicy, error)
	// UpdateAccessPolicy changes the description and the standalone flag;
	// nil keeps a field.
	UpdateAccessPolicy(ctx context.Context, name string, description *string, standalone *bool) (AccessPolicy, error)
	// SetAccessPolicyInterface permits iface at position order, or denies
	// it, and returns the policy afterwards.
	// order -1 puts a permitted interface last. A write that went through
	// but could not be read back is not an error: the result carries the
	// policy as it was and says so (AccessPolicy.Unconfirmed).
	SetAccessPolicyInterface(ctx context.Context, name, iface string, permit bool, order int, expectOrder []string) (AccessPolicy, error)
	// AssignDevicePolicy moves a device (by MAC) to a policy; "" returns it
	// to the router's default.
	AssignDevicePolicy(ctx context.Context, mac, policy string) (Device, error)
	ListDevices(ctx context.Context) ([]Device, error)

	GetLogs(ctx context.Context, q LogsQuery) ([]LogEntry, int, error) // entries, total matched
	TestConnectivity(ctx context.Context, tunnelID string) (ConnectivityResult, error)
	// CheckIP fetches the external IP through the tunnel and over the bare
	// WAN. A tunnel that is not running is an error, not an empty result.
	CheckIP(ctx context.Context, tunnelID string) (IPCheckResult, error)
	MonitoringMatrix(ctx context.Context) (MonitoringMatrix, error)
	// ListConnections returns one page of open flows and the total that
	// matched, so a short page cannot be read as a quiet network.
	ListConnections(ctx context.Context, q ConnectionsQuery) (page []Connection, total int, err error)
	// ListSingboxConnections returns one page of the connections sing-box
	// carries, read once from its Clash API, and the total that matched. An
	// engine that does not answer is an error, not an empty list.
	ListSingboxConnections(ctx context.Context, q SingboxConnectionsQuery) (page []SingboxConnection, total int, err error)
	// ExplainSingboxRoute judges a destination against the router's rules
	// (domain "" for a literal address; ips are its IPv4 addresses).
	// clientIP may be empty.
	ExplainSingboxRoute(ctx context.Context, domain string, ips []string, clientIP string) (SingboxRouteExplanation, error)
	// PingCheckLogs returns the health-check journal, newest first.
	PingCheckLogs(ctx context.Context, tunnelID string, limit int) ([]PingCheckLogEntry, error)
	// RunDiagnostics starts a sweep without waiting for it. includeRestart
	// adds the restart test, which stops and starts every running kernel
	// tunnel; without it the sweep changes nothing.
	RunDiagnostics(ctx context.Context, includeRestart bool) (DiagnosticsRun, error)
	// RunTunnelDiagnostics checks one tunnel (an AWG tunnel id, or
	// singbox:<tag> for a proxy) and waits for the result: a single-tunnel
	// run keeps no report for get_diagnostics. Refused while another sweep
	// runs.
	RunTunnelDiagnostics(ctx context.Context, tunnelID string) ([]DiagnosticsCheck, error)
	// DiagnosticsResult summarises the last completed sweep; includePassed
	// adds every check. With none, it
	// returns ErrNoDiagnostics rather than an empty, reassuring report.
	DiagnosticsResult(ctx context.Context, includePassed bool) (DiagnosticsResult, error)
	// RunPingCheck starts a check of every monitored tunnel without
	// waiting for it and returns the last completed statuses.
	RunPingCheck(ctx context.Context) (PingCheckRun, error)

	// ResolveDomain looks a domain up (IPv4 only), for explain_route's
	// subnet comparison. A lookup failure is returned as an error; the
	// tool degrades rather than failing the whole call.
	ResolveDomain(ctx context.Context, domain string) ([]string, error)

	ListManagedServers(ctx context.Context) ([]ManagedServer, error)
	ListServerPeers(ctx context.Context, serverID string) ([]ServerPeer, error)
	// AddServerPeer creates a client and returns it. An empty TunnelIP
	// means "allocate one"; the implementation must not leave that choice
	// to the caller.
	AddServerPeer(ctx context.Context, in AddPeerInput) (ServerPeer, error)
	SetServerPeerEnabled(ctx context.Context, serverID, publicKey string, enabled bool) (ServerPeer, error)
	// ServerPeerConfig renders the client .conf, private key included.
	ServerPeerConfig(ctx context.Context, serverID, publicKey string) (string, error)
	ControlSingbox(ctx context.Context, action string) (SingboxStatus, error)
	ListSingboxTunnels(ctx context.Context) ([]SingboxTunnel, error)
	// ListSingboxRules returns the router's rules in evaluation order.
	// hasDraft reports whether they come from an unapplied draft. Like
	// every router read here (groups, DNS, rule sets), it reads the
	// configuration the router routes by: in fakeip-tun mode that mode's
	// own, which has no draft.
	ListSingboxRules(ctx context.Context) (rules []SingboxRule, hasDraft bool, err error)
	// ListSingboxOutbounds returns every group. An engine that does not
	// answer is not an error: the entries come back with RuntimeKnown
	// false and no active member. hasDraft reports whether the router
	// holds an unapplied draft; groups are listed from it when it exists.
	ListSingboxOutbounds(ctx context.Context) (groups []SingboxOutbound, hasDraft bool, err error)
	// GetSingboxOutbound returns one group with all its members, in
	// configuration order, and HasDraft as ListSingboxOutbounds reports
	// it. An unknown tag is an error; so is a tag that names a single
	// server, and the error says which it is.
	GetSingboxOutbound(ctx context.Context, tag string) (SingboxOutboundDetail, error)
	SingboxStaging(ctx context.Context) (SingboxStaging, error)
	// SetSingboxRuleOutbound retargets one rule. The write lands in the
	// draft; nothing changes for traffic until ApplySingboxStaging. Refused
	// in fakeip-tun mode, as the writes below are. expectMatch, when not
	// empty, is the rule's match from ListSingboxRules: the write is refused
	// unless the rule at index still renders so, checked in the same write.
	SetSingboxRuleOutbound(ctx context.Context, index int, expectMatch, outbound string) error
	// ListSingboxDNSServers returns the router's DNS servers in
	// configuration order. hasDraft is ListSingboxRules's: the list comes
	// from the draft when one exists.
	ListSingboxDNSServers(ctx context.Context) (servers []SingboxDNSServer, hasDraft bool, err error)
	// SingboxRouterStatus describes how the router is set up and whether
	// it works. It asks NDMS and probes the kernel, as the web page does,
	// so it is slower than the lists.
	SingboxRouterStatus(ctx context.Context) (SingboxRouterStatus, error)
	// ListSingboxDNSRules returns the router's DNS rules in evaluation
	// order with the DNS final and strategy, hasDraft as
	// ListSingboxDNSServers.
	ListSingboxDNSRules(ctx context.Context) (SingboxDNSRules, error)
	// ListSingboxDNSRewrites returns the DNS Rewrite entries in order.
	ListSingboxDNSRewrites(ctx context.Context) ([]SingboxDNSRewrite, error)
	// ListSingboxRuleSets returns every rule set in configuration order,
	// hasDraft as ListSingboxDNSServers. The tool pages the result;
	// implementations must not truncate it.
	ListSingboxRuleSets(ctx context.Context) (sets []SingboxRuleSet, hasDraft bool, err error)

	// The four router writes below land in the draft, as
	// SetSingboxRuleOutbound does, and are refused while the router runs in
	// fakeip-tun mode: that mode routes by a configuration of its own, so an
	// edit here would be applied to one nothing reads.

	// CreateSingboxOutboundGroup adds a selector, urltest or fallback group
	// to the router. A tag already taken and a member that is not a known
	// outbound are errors.
	CreateSingboxOutboundGroup(ctx context.Context, in SingboxGroupInput) error
	// SetSingboxOutboundGroupMembers replaces a router group's members,
	// keeping their order and the group's other settings. A subscription's
	// group is an error that names the tools that change it.
	SetSingboxOutboundGroupMembers(ctx context.Context, tag string, members []string) error
	// SetSingboxDNSServerDetour points one DNS server at an outbound; ""
	// dials it directly again.
	SetSingboxDNSServerDetour(ctx context.Context, tag, detour string) error
	// SetSingboxRuleSetDetour sets the download detour of every rule set in
	// tags, all or none; "" downloads them directly again. Only remote sets
	// are downloaded, so any other kind is an error.
	SetSingboxRuleSetDetour(ctx context.Context, tags []string, detour string) error

	// The rule and rule set writes below land in the draft and are refused
	// in fakeip-tun mode, as the writes above are. System rules (sniff,
	// hijack-dns, the private-address bypass) and rules awg-manager manages
	// are neither edited nor moved, and nothing is put above a system rule.

	// Positions come from ListSingboxRules and shift with every insert,
	// move and delete. expectMatch is checked as SetSingboxRuleOutbound
	// checks it, against the rule the edit touches: the one at index, at
	// from, or the one now at position that the new rule pushes down.

	// AddSingboxRule inserts a rule at position; -1 appends it, and then
	// there is nothing to check expectMatch against.
	AddSingboxRule(ctx context.Context, position int, expectMatch string, in SingboxRuleInput) error
	// UpdateSingboxRule replaces rule index whole.
	UpdateSingboxRule(ctx context.Context, index int, expectMatch string, in SingboxRuleInput) error
	// DeleteSingboxRule removes rule index and returns it as it was.
	DeleteSingboxRule(ctx context.Context, index int, expectMatch string) (SingboxRule, error)
	// MoveSingboxRule moves rule from to position to.
	MoveSingboxRule(ctx context.Context, from, to int, expectMatch string) error
	// SetSingboxRouteFinal sets the outbound for traffic no rule matched.
	SetSingboxRouteFinal(ctx context.Context, outbound string) error
	// AddSingboxRuleSet adds a rule set; a tag already taken is an error.
	AddSingboxRuleSet(ctx context.Context, in SingboxRuleSetInput) error
	// DeleteSingboxRuleSet removes a rule set and returns it as it was. A
	// set rules still use is refused unless force; with force the
	// references go, and a rule left without any matcher goes with them —
	// affected are those rules as they were.
	DeleteSingboxRuleSet(ctx context.Context, tag string, force bool) (SingboxRuleSetDeletion, error)

	ApplySingboxStaging(ctx context.Context) error
	DiscardSingboxStaging(ctx context.Context) error
	// CheckSingboxDelay probes one outbound: a proxy, a subscription
	// server or a group. The tag is classified from configuration, so an
	// unknown tag is an error even while the engine is down, and a tag
	// that exists but is not an outbound (an excluded server) is an error
	// that says why. An engine that does not answer, or answers without
	// the tag, is an error too: nothing is probed, because the prober
	// answers 0 then, the same 0 as for a silent outbound. An outbound
	// that stays silent is a result with Reachable false. A group is
	// probed through its active member.
	CheckSingboxDelay(ctx context.Context, tag string) (SingboxDelay, error)
	// ListSingboxSubscriptions returns every subscription, sorted by label
	// and then id, so that an offset means the same thing on every call.
	// The tool pages the result; implementations must not truncate it.
	ListSingboxSubscriptions(ctx context.Context) ([]SingboxSubscription, error)

	// SetSingboxSubscriptionEnabled switches a subscription and returns it
	// as it stands afterwards. warnings names the aggregate groups whose
	// members changed; a call that changes nothing returns none. An
	// unknown id is an error and nothing is written.
	SetSingboxSubscriptionEnabled(ctx context.Context, id string, enabled bool) (updated SingboxSubscription, warnings []string, err error)

	// SetSingboxSubscriptionMode switches a subscription's group between
	// selector, urltest and fallback and returns the subscription as it
	// stands afterwards. mode arrives trimmed and lowercased; anything else
	// is an error. A call that changes nothing writes nothing. An
	// unknown id is an error and nothing is written.
	SetSingboxSubscriptionMode(ctx context.Context, id, mode string) (SingboxSubscription, error)

	// SetSingboxSubscriptionActiveMember makes memberTag the server a
	// selector-mode subscription routes through and returns the
	// subscription as read before the switch: SingboxSubscription carries no
	// active server, so the switch changes none of its fields. A urltest or
	// fallback subscription, an unknown id and a tag that is not a server of the
	// subscription's group are errors, and nothing is written.
	SetSingboxSubscriptionActiveMember(ctx context.Context, id, memberTag string) (SingboxSubscription, error)

	// CreateSingboxAggregateGroup creates an enabled group over the servers
	// of the given subscriptions and applies it at once: no draft, a sing-box
	// reload, and a Keenetic ProxyN interface when NDMS proxy mode is on. An
	// unknown subscription id or a tag already taken is an error and nothing
	// is left behind.
	CreateSingboxAggregateGroup(ctx context.Context, in SingboxAggregateGroupInput) (SingboxAggregateGroup, error)

	// CreateSingboxSubscription creates an enabled subscription and fetches
	// it at once; a fetch that fails or yields no server leaves nothing
	// behind. The URL and the links carry the provider's token or the
	// servers' credentials: implementations never put them, or the service's
	// text about them, into an error or the journal.
	CreateSingboxSubscription(ctx context.Context, in SingboxSubscriptionInput) (SingboxSubscription, error)
	// DeleteSingboxSubscription removes a subscription with its group and
	// servers and returns it as it was. A subscription whose group or
	// servers routing rules or the device proxy still use is refused, as in
	// the web interface.
	DeleteSingboxSubscription(ctx context.Context, id string) (SingboxSubscription, error)
	// RefreshSingboxSubscription fetches a subscription now and waits for
	// the result.
	RefreshSingboxSubscription(ctx context.Context, id string) (SingboxRefreshResult, error)
	// UpdateSingboxSubscriptionSettings applies the patch and returns the
	// subscription as it stands afterwards. A filter change refetches it.
	UpdateSingboxSubscriptionSettings(ctx context.Context, p SingboxSubscriptionPatch) (SingboxSubscription, error)
	// ExcludeSingboxSubscriptionServers takes servers out of a fetched
	// subscription's group for good (a refresh keeps them out);
	// RestoreSingboxSubscriptionServers brings them back and refetches.
	ExcludeSingboxSubscriptionServers(ctx context.Context, id string, tags []string) (SingboxSubscription, error)
	RestoreSingboxSubscriptionServers(ctx context.Context, id string, tags []string) (SingboxSubscription, error)

	// AddSingboxTunnel adds one hand-configured proxy from a share link and
	// returns it. The link carries credentials and is handled as
	// AddSingboxSubscriptionServer handles one.
	AddSingboxTunnel(ctx context.Context, shareLink string) (SingboxTunnel, []string, error)
	// DeleteSingboxTunnel removes a proxy and returns it as it was; one
	// that routing rules, groups or the device proxy still use is refused.
	DeleteSingboxTunnel(ctx context.Context, tag string) (SingboxTunnel, error)

	// AddSingboxSubscriptionServer adds one server, given as a share link,
	// to a pasted (inline) subscription and applies it at once. The link
	// carries the server's credentials: implementations never put it, or
	// text derived from it, into an error or the journal. A link the
	// subscription does not take as a server is an error, even where the
	// service records it among the rejected entries and returns no error.
	AddSingboxSubscriptionServer(ctx context.Context, id, shareLink string) (SingboxServerAdded, error)

	OpenAPISpec() []byte
}
