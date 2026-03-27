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
)

// Subgroups
const (
	SubLifecycle    = "lifecycle"
	SubOps          = "ops"
	SubState        = "state"
	SubFirewall     = "firewall"
	SubPingcheck     = "pingcheck"
	SubConnectivity  = "connectivity"
	SubDnsRoute     = "dns-route"
	SubStaticRoute  = "static-route"
	SubAccessPolicy = "access-policy"
	SubClientRoute  = "client-route"
	SubManaged      = "managed"
	SubSystemTunnel = "system-tunnels"
	SubBoot         = "boot"
	SubWan          = "wan"
	SubAuth         = "auth"
	SubSettings     = "settings"
	SubUpdate       = "update"
)

// LogEntry represents a single log entry.
type LogEntry struct {
	Timestamp time.Time `json:"timestamp"`
	Level     string    `json:"level"`
	Group     string    `json:"group"`
	Subgroup  string    `json:"subgroup,omitempty"`
	Action    string    `json:"action"`
	Target    string    `json:"target"`
	Message   string    `json:"message"`
}
