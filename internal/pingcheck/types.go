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
	StateChange string    `json:"stateChange"` // "dead", "alive", or ""
}

// TunnelStatus represents the current ping check status of a tunnel.
type TunnelStatus struct {
	TunnelID       string     `json:"tunnelId"`
	TunnelName     string     `json:"tunnelName"`
	Enabled        bool       `json:"enabled"`
	Status         string     `json:"status"` // "alive", "dead", "disabled", "paused"
	Method         string     `json:"method"` // "http" or "icmp"
	LastCheck      *time.Time `json:"lastCheck"`
	LastLatency    int        `json:"lastLatency"` // ms
	FailCount      int        `json:"failCount"`
	FailThreshold  int        `json:"failThreshold"`
	IsDeadByMonitor bool      `json:"isDeadByMonitor"`
}
