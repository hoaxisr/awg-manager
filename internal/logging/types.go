package logging

import "time"

// Log levels
const (
	LevelInfo  = "info"
	LevelWarn  = "warn"
	LevelError = "error"
)

// Log categories
const (
	CategoryTunnel   = "tunnel"
	CategorySettings = "settings"
	CategorySystem   = "system"
	CategoryDnsRoute = "dns-route"
)

// LogEntry represents a single log entry.
type LogEntry struct {
	Timestamp time.Time `json:"timestamp"`
	Level     string    `json:"level"`
	Category  string    `json:"category"`
	Action    string    `json:"action"`
	Target    string    `json:"target"`
	Message   string    `json:"message"`
	Error     string    `json:"error,omitempty"`
}
