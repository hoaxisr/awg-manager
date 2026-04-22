package events

// Event represents a server-sent event.
type Event struct {
	ID   uint64 `json:"-"`    // monotonic, sent as SSE "id:" field
	Type string `json:"type"` // SSE event type (e.g. "tunnel:state")
	Data any    `json:"data"` // JSON-serializable payload
}

// Tunnel lifecycle payloads.

// TunnelStateEvent is an internal dual-publish payload consumed by the
// connectivity monitor when the orchestrator reports a state transition.
// NOT forwarded to SSE clients — the frontend polls tunnels and reacts
// to resource:invalidated hints instead.
type TunnelStateEvent struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	State   string `json:"state"`
	Backend string `json:"backend,omitempty"`
}

// TunnelDeletedEvent is an internal dual-publish payload emitted by the
// orchestrator after tunnel deletion. NOT forwarded to SSE clients.
type TunnelDeletedEvent struct {
	ID string `json:"id"`
}

// PingCheckStateEvent is an internal dual-publish payload emitted by the
// ping-check monitors and consumed by dnsroute failover. NOT forwarded
// to SSE clients — the frontend polls the ping-check status list.
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
	FileType   string `json:"fileType"`   // "geosite" | "geoip"
	Downloaded int64  `json:"downloaded"` // bytes received so far
	Total      int64  `json:"total"`      // 0 when unknown
	Phase      string `json:"phase"`      // "download" | "validate" | "done" | "error"
	Error      string `json:"error,omitempty"`
}

// ResourceInvalidatedEvent is the single state-invalidation hint.
// Replaces all per-resource state events (tunnel:state, server:updated,
// routing:*-updated, singbox:status, singbox:tunnel, pingcheck:state,
// tunnels:list, snapshot:*). The client uses Resource to look up the
// corresponding polling store and trigger an immediate refetch.
// Payload intentionally carries nothing beyond the key — the store is
// the single source of truth for data shape, and the client always
// re-reads via REST.
type ResourceInvalidatedEvent struct {
	Resource string `json:"resource"`
	// Reason is optional and for backend logs / debug; the frontend
	// does not key off it. Examples: "tunnel-toggled", "ndms-restart".
	Reason string `json:"reason,omitempty"`
}
