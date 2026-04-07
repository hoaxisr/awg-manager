package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// SnapshotBuilder collects current state from all services for SSE snapshots.
type SnapshotBuilder struct {
	tunnels         *TunnelsHandler
	external        *ExternalTunnelsHandler
	systemTun       *SystemTunnelsHandler
	servers         *ServersHandler
	managed         *ManagedServerHandler
	pingCheck       *PingCheckHandler
	logging         *LoggingHandler
	routingSnapshot func(ctx context.Context) interface{} // full routing data (Task 6)
	systemSnapshot  func(ctx context.Context) interface{} // system info snapshot
	bootInProgress  func() bool
	wanIP           func(ctx context.Context) string
	instanceID      string
}

// NewSnapshotBuilder creates a new SnapshotBuilder.
func NewSnapshotBuilder() *SnapshotBuilder {
	return &SnapshotBuilder{}
}

// SetTunnelsHandler sets the tunnels handler reference.
func (sb *SnapshotBuilder) SetTunnelsHandler(h *TunnelsHandler) { sb.tunnels = h }

// SetExternalHandler sets the external tunnels handler reference.
func (sb *SnapshotBuilder) SetExternalHandler(h *ExternalTunnelsHandler) { sb.external = h }

// SetSystemTunnelsHandler sets the system tunnels handler reference.
func (sb *SnapshotBuilder) SetSystemTunnelsHandler(h *SystemTunnelsHandler) { sb.systemTun = h }

// SetServersHandler sets the servers handler reference.
func (sb *SnapshotBuilder) SetServersHandler(h *ServersHandler) { sb.servers = h }

// SetManagedHandler sets the managed server handler reference.
func (sb *SnapshotBuilder) SetManagedHandler(h *ManagedServerHandler) { sb.managed = h }

// SetPingCheckHandler sets the ping check handler reference.
func (sb *SnapshotBuilder) SetPingCheckHandler(h *PingCheckHandler) { sb.pingCheck = h }

// SetLoggingHandler sets the logging handler reference.
func (sb *SnapshotBuilder) SetLoggingHandler(h *LoggingHandler) { sb.logging = h }

// SetBootStatusFunc sets the callback to check boot status.
func (sb *SnapshotBuilder) SetBootStatusFunc(fn func() bool) {
	sb.bootInProgress = fn
}

// SetSystemSnapshotFunc sets the callback to collect system info.
func (sb *SnapshotBuilder) SetSystemSnapshotFunc(fn func(ctx context.Context) interface{}) {
	sb.systemSnapshot = fn
}

// SetRoutingSnapshotFunc sets the callback to collect routing snapshot data.
func (sb *SnapshotBuilder) SetRoutingSnapshotFunc(fn func(ctx context.Context) interface{}) {
	sb.routingSnapshot = fn
}

// SetWANIPFunc sets the callback to get the WAN IP address.
func (sb *SnapshotBuilder) SetWANIPFunc(fn func(ctx context.Context) string) {
	sb.wanIP = fn
}

// SetInstanceID sets the server instance ID for version detection.
func (sb *SnapshotBuilder) SetInstanceID(id string) {
	sb.instanceID = id
}

// SendSnapshots sends all current-state snapshots to an SSE client.
// Called immediately after SSE connection (or reconnection).
func (sb *SnapshotBuilder) SendSnapshots(w http.ResponseWriter, flusher http.Flusher, ctx context.Context) {
	// Check boot status
	if sb.bootInProgress != nil && sb.bootInProgress() {
		writeSSE(w, flusher, "system:booting", map[string]interface{}{
			"phase": "starting",
		})
		return
	}

	writeSSE(w, flusher, "system:ready", map[string]interface{}{"ok": true, "instanceId": sb.instanceID})

	snapCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	// System info snapshot
	if sb.systemSnapshot != nil {
		data := sb.systemSnapshot(snapCtx)
		if data != nil {
			writeSSE(w, flusher, "snapshot:system", data)
		}
	}

	// Tunnels snapshot
	if sb.tunnels != nil {
		items, err := sb.tunnels.listItems(snapCtx)
		if err == nil {
			payload := map[string]interface{}{
				"tunnels": items,
			}
			if sb.external != nil {
				external, _ := sb.external.listExternal(snapCtx)
				payload["external"] = external
			}
			if sb.systemTun != nil {
				system, _ := sb.systemTun.listSystemTunnels(snapCtx)
				payload["system"] = system
			}
			writeSSE(w, flusher, "snapshot:tunnels", payload)
		}
	}

	// Servers snapshot
	if sb.servers != nil {
		servers, _ := sb.servers.listServers(snapCtx)
		payload := map[string]interface{}{
			"servers": servers,
		}
		if sb.managed != nil {
			managed := sb.managed.getManaged()
			payload["managed"] = managed
			if managed != nil {
				payload["managedStats"] = sb.managed.getManagedStats(snapCtx)
			}
		}
		if sb.wanIP != nil {
			payload["wanIP"] = sb.wanIP(snapCtx)
		} else {
			payload["wanIP"] = ""
		}
		writeSSE(w, flusher, "snapshot:servers", payload)
	}

	// Routing snapshot
	if sb.routingSnapshot != nil {
		data := sb.routingSnapshot(snapCtx)
		if data != nil {
			writeSSE(w, flusher, "snapshot:routing", data)
		}
	}

	// PingCheck snapshot
	if sb.pingCheck != nil {
		statuses, logs := sb.pingCheck.collectAll()
		writeSSE(w, flusher, "snapshot:pingcheck", map[string]interface{}{
			"statuses": statuses,
			"logs":     logs,
		})
	}

	// Logs snapshot
	if sb.logging != nil {
		data := sb.logging.collectSnapshot()
		writeSSE(w, flusher, "snapshot:logs", data)
	}
}

// writeSSE writes a single SSE event.
func writeSSE(w http.ResponseWriter, flusher http.Flusher, eventType string, data interface{}) {
	jsonData, err := json.Marshal(data)
	if err != nil {
		return
	}
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", eventType, jsonData)
	flusher.Flush()
}
