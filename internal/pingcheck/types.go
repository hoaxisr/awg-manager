package pingcheck

import "time"

// CheckResult represents the result of a single connectivity check.
type CheckResult struct {
	Success bool
	Latency int    // milliseconds
	Error   string // error message if failed
}

// LogEntry represents a single log entry for a ping check.
type LogEntry struct {
	Timestamp   time.Time `json:"timestamp"`
	TunnelID    string    `json:"tunnelId"`
	TunnelName  string    `json:"tunnelName"`
	Success     bool      `json:"success"`
	Latency     int       `json:"latency"`     // ms, 0 if failed
	Error       string    `json:"error"`       // error message if failed
	FailCount   int       `json:"failCount"`   // current fail count (e.g., 2)
	Threshold   int       `json:"threshold"`   // fail threshold (e.g., 3)
	StateChange string    `json:"stateChange"` // "link_toggle", "recovered", or ""
	Backend     string    `json:"backend"`     // "kernel" or "nativewg"
}

// TunnelStatus represents the current ping check status of a tunnel.
type TunnelStatus struct {
	TunnelID      string     `json:"tunnelId"`
	TunnelName    string     `json:"tunnelName"`
	Enabled       bool       `json:"enabled"`
	Backend       string     `json:"backend"` // "kernel" or "nativewg"
	Status        string     `json:"status"`  // "alive", "recovering", "disabled", "stopped"
	Method        string     `json:"method"`  // "http", "icmp", "connect", "tls", "uri"
	LastCheck     *time.Time `json:"lastCheck"`
	LastLatency   int        `json:"lastLatency"` // ms
	FailCount     int        `json:"failCount"`
	SuccessCount  int        `json:"successCount,omitempty"` // NDMS native only
	FailThreshold int        `json:"failThreshold"`
	RestartCount  int        `json:"restartCount"`
	TunnelRunning bool       `json:"tunnelRunning"` // whether the tunnel interface is actually up
}

// TunnelPingInfo is a lightweight status for embedding in tunnel list responses.
// Zero value (Status="") means "disabled" — callers should treat empty status as disabled.
type TunnelPingInfo struct {
	Status        string `json:"status"`        // "alive", "recovering", "disabled"
	RestartCount  int    `json:"restartCount"`  // link toggle attempts
	FailCount     int    `json:"failCount"`     // current consecutive failures
	FailThreshold int    `json:"failThreshold"` // threshold for link toggle
	Enabled       bool   `json:"enabled"`       // whether monitoring is enabled
	IntervalSec   int    `json:"intervalSec"`   // check interval in seconds
}
