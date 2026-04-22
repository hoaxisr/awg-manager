package events

import "time"

// Event represents a server-sent event.
type Event struct {
	ID   uint64 `json:"-"`    // monotonic, sent as SSE "id:" field
	Type string `json:"type"` // SSE event type (e.g. "tunnel:state")
	Data any    `json:"data"` // JSON-serializable payload
}

// Tunnel lifecycle payloads.

// TunnelStateEvent is sent when tunnel state changes (start/stop).
type TunnelStateEvent struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	State   string `json:"state"`
	Backend string `json:"backend,omitempty"`
}

// TunnelDeletedEvent is sent when a tunnel is deleted.
type TunnelDeletedEvent struct {
	ID string `json:"id"`
}

// TunnelCreatedEvent is sent when a new tunnel is created or imported.
type TunnelCreatedEvent struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Backend string `json:"backend"`
}

// TunnelUpdatedEvent is sent when tunnel config is updated.
type TunnelUpdatedEvent struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// PingCheckStateEvent is sent when ping-check status changes.
type PingCheckStateEvent struct {
	TunnelID        string `json:"tunnelId"`
	Status          string `json:"status"`
	FailCount       int    `json:"failCount"`
	SuccessCount    int    `json:"successCount"`
	RestartDetected bool   `json:"restartDetected,omitempty"`
}

// LogEntryEvent is sent for each new log entry.
type LogEntryEvent struct {
	Timestamp string `json:"timestamp"`
	Level     string `json:"level"`
	Group     string `json:"group"`
	Subgroup  string `json:"subgroup,omitempty"`
	Action    string `json:"action"`
	Target    string `json:"target"`
	Message   string `json:"message"`
}

// Traffic update payload (sent by Traffic Collector).
type TunnelTrafficEvent struct {
	ID            string `json:"id"`
	RxBytes       int64  `json:"rxBytes"`
	TxBytes       int64  `json:"txBytes"`
	LastHandshake string `json:"lastHandshake,omitempty"`
	StartedAt     string `json:"startedAt,omitempty"`
}

// Connectivity check result (sent by Connectivity Monitor).
type TunnelConnectivityEvent struct {
	ID        string `json:"id"`
	Connected bool   `json:"connected"`
	Latency   *int   `json:"latency"`
}

// Ping check log entry (sent by PingCheck service).
type PingCheckLogEvent struct {
	Timestamp   string `json:"timestamp"`
	TunnelID    string `json:"tunnelId"`
	TunnelName  string `json:"tunnelName"`
	Success     bool   `json:"success"`
	Latency     int    `json:"latency"`
	Error       string `json:"error"`
	FailCount   int    `json:"failCount"`
	Threshold   int    `json:"threshold"`
	StateChange string `json:"stateChange"`
	Backend     string `json:"backend,omitempty"`
}

// SingboxTunnelEvent is emitted when sing-box tunnels are added/updated/removed.
type SingboxTunnelEvent struct {
	Action string   `json:"action"` // "added" | "updated" | "removed"
	Tags   []string `json:"tags"`
}

// SingboxStatusEvent is emitted after install/reconcile operations.
// Mirrors the REST /api/singbox/status payload so the UI reducer can
// reuse the same handler for both the initial fetch and live updates.
type SingboxStatusEvent struct {
	Installed      bool     `json:"installed"`
	Running        bool     `json:"running"`
	Version        string   `json:"version,omitempty"`
	PID            int      `json:"pid,omitempty"`
	TunnelCount    int      `json:"tunnelCount"`
	ProxyComponent bool     `json:"proxyComponent"`
	Features       []string `json:"features,omitempty"`
}

// SingboxDelayEvent is emitted when a sing-box tunnel delay is measured.
type SingboxDelayEvent struct {
	Tag       string `json:"tag"`
	Delay     int    `json:"delay"`     // milliseconds; 0 = timeout
	Timestamp int64  `json:"timestamp"` // unix seconds
}

// DNSRouteFailoverEvent is sent when DNS route failover switches targets,
// restores them, or fails to apply changes.
type DNSRouteFailoverEvent struct {
	ListID     string `json:"listId"`
	ListName   string `json:"listName"`
	TunnelID   string `json:"tunnelId"`
	FromTunnel string `json:"fromTunnel,omitempty"`
	ToTunnel   string `json:"toTunnel,omitempty"`
	Action     string `json:"action"` // "switched" | "restored" | "error"
	Error      string `json:"error,omitempty"`
}

// GeoDownloadProgressEvent reports the live state of a geo .dat download.
// Total may be 0 when the server didn't send a Content-Length header.
type GeoDownloadProgressEvent struct {
	URL        string `json:"url"`
	FileType   string `json:"fileType"`             // "geosite" | "geoip"
	Downloaded int64  `json:"downloaded"`           // bytes received so far
	Total      int64  `json:"total"`                // 0 when unknown
	Phase      string `json:"phase"`                // "download" | "validate" | "done" | "error"
	Error      string `json:"error,omitempty"`
}

// SaveStatusEvent is published by the NDMS SaveCoordinator on every state
// transition so the UI can show a Google-Docs-style save indicator.
//
// State is one of: "idle" | "pending" | "saving" | "error" | "failed".
type SaveStatusEvent struct {
	State        string    `json:"state"`
	LastError    string    `json:"lastError,omitempty"`
	LastSaveAt   time.Time `json:"lastSaveAt,omitempty"`
	PendingCount int       `json:"pendingCount"`
}
