package logging

import "time"

// Level represents log verbosity.
type Level string

const (
	LevelError Level = "error"
	LevelWarn  Level = "warn"
	LevelInfo  Level = "info"
	LevelFull  Level = "full"
	LevelDebug Level = "debug"
)

var levelPriority = map[Level]int{
	LevelError: 0, LevelWarn: 0, LevelInfo: 1, LevelFull: 2, LevelDebug: 3,
}

// IsVisible returns true if entryLevel should be shown at configuredLevel.
// ERROR and WARN are always visible.
func IsVisible(entryLevel, configuredLevel Level) bool {
	if entryLevel == LevelError || entryLevel == LevelWarn {
		return true
	}
	return levelPriority[entryLevel] <= levelPriority[configuredLevel]
}

// Groups
const (
	GroupTunnel  = "tunnel"
	GroupRouting = "routing"
	GroupServer  = "server"
	GroupSystem  = "system"
	GroupSingbox = "singbox"
)

// Subgroups — app-buckets (tunnel/routing/server/system)
const (
	SubLifecycle      = "lifecycle"
	SubOps            = "ops"
	SubState          = "state"
	SubFirewall       = "firewall"
	SubPingcheck      = "pingcheck"
	SubConnectivity   = "connectivity"
	SubTest           = "test"
	SubSignature      = "signature"
	SubDnsRoute       = "dns-route"
	SubStaticRoute    = "static-route"
	SubAccessPolicy   = "access-policy"
	SubClientRoute    = "client-route"
	SubSingboxRouter  = "singbox-router"
	SubBypassSet      = "bypass-set"
	SubSubscription   = "subscription"
	SubDeviceProxy    = "deviceproxy"
	SubHrNeo          = "hrneo"
	SubRoutingCatalog = "catalog"
	SubAWGOutbounds   = "awg-outbounds"
	SubManaged        = "managed"
	SubSystemTunnel   = "system-tunnels"
	SubBoot           = "boot"
	SubWan            = "wan"
	SubAuth           = "auth"
	SubMcp            = "mcp"
	SubSettings       = "settings"
	SubUpdate         = "update"
	SubCleanup        = "cleanup"
	SubDnsCheck       = "dnscheck"
	SubConnections    = "connections"
	SubTraffic        = "traffic"
	SubDiagnostics    = "diagnostics"
	SubProfiling      = "profiling" // slow HTTP telemetry (handlers → UI journal)
	SubRCI            = "rci"
	SubNDMS           = "ndms"
	SubOrchestrator   = "orchestrator" // tunnel lifecycle decisions (decide/execute/external-restart)
	SubKmod           = "kmod"         // awg_proxy kernel module load/add/remove
	SubStorage        = "storage"      // tunnel store + settings persistence
	SubHTTP           = "http"         // HTTP server lifecycle and listener events
	SubMonitoring     = "monitoring"   // matrix probe transitions (ok→fail / restore)

	// Singbox bucket subgroups
	SubSBInbound  = "inbound"
	SubSBOutbound = "outbound"
	SubSBDNS      = "dns"
	SubSBRouter   = "router"
	SubSBRuntime  = "runtime"
	SubSBProcess  = "process"
	SubAwg3       = "awg3" // imported AWG3 endpoint projection into 16-awg3.json
)

// Bucket identifies which buffer a log entry belongs to. Sing-box logs are
// isolated from app logs so a noisy forwarder cannot evict tunnel/routing
// history from the same ring buffer.
type Bucket string

const (
	BucketApp     Bucket = "app"
	BucketSingbox Bucket = "singbox"
)

// BucketForGroup returns which bucket receives entries from the given group.
// All groups except `singbox` go to the app bucket.
func BucketForGroup(group string) Bucket {
	if group == GroupSingbox {
		return BucketSingbox
	}
	return BucketApp
}

// KnownSubgroups is the static catalog of subgroups per group used by the
// frontend to render the second-row chip filter. Order is presentation-stable
// (alphabetical within group except where a domain ordering matters).
var KnownSubgroups = map[string][]string{
	GroupTunnel: {
		SubLifecycle, SubOrchestrator, SubOps, SubKmod, SubState, SubFirewall,
		SubPingcheck, SubConnectivity, SubTest, SubSignature,
	},
	GroupRouting: {
		SubDnsRoute, SubStaticRoute, SubAccessPolicy, SubClientRoute,
		SubSingboxRouter, SubBypassSet, SubSubscription, SubDeviceProxy, SubHrNeo, SubRoutingCatalog,
		SubAWGOutbounds,
	},
	GroupServer: {
		SubHTTP, SubManaged,
	},
	GroupSystem: {
		SubBoot, SubAuth, SubMcp, SubSettings, SubUpdate, SubWan, SubSystemTunnel,
		SubCleanup, SubDnsCheck, SubConnections, SubTraffic, SubDiagnostics,
		SubProfiling, SubRCI, SubNDMS, SubStorage, SubMonitoring,
	},
	GroupSingbox: {
		SubSBProcess, SubSBInbound, SubSBOutbound, SubSBDNS, SubSBRouter, SubSBRuntime,
	},
}

// LogEntry represents a single log entry.
//
// Идентичные повторы схлопываются в одну запись (см. LogBuffer.Coalesce):
// Timestamp — первое появление, Repeats — сколько повторов свёрнуто
// (0 = запись уникальна), LastSeen — время последнего повтора.
type LogEntry struct {
	Timestamp time.Time  `json:"timestamp"`
	Level     string     `json:"level"`
	Group     string     `json:"group"`
	Subgroup  string     `json:"subgroup,omitempty"`
	Action    string     `json:"action"`
	Target    string     `json:"target"`
	Message   string     `json:"message"`
	Repeats   int        `json:"repeats,omitempty"`
	LastSeen  *time.Time `json:"lastSeen,omitempty"`
}
