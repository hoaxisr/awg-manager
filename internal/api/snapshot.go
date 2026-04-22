package api

import "context"

// TunnelsSnapshotBuilder composes the {tunnels, external, system}
// payload used by GET /api/tunnels/all and by the hook-driven
// resource:invalidated refresher. It is the single assembly point
// for the composite tunnels list the polling store reads.
//
// The struct is a thin holder for the three handler references;
// callers wire whichever handlers are available and call Build.
// Missing handlers produce empty slices in the relevant keys.
type TunnelsSnapshotBuilder struct {
	tunnels   *TunnelsHandler
	external  *ExternalTunnelsHandler
	systemTun *SystemTunnelsHandler
}

// NewTunnelsSnapshotBuilder creates a new builder with no handlers
// wired. Use the setters to wire the three handler references.
func NewTunnelsSnapshotBuilder() *TunnelsSnapshotBuilder {
	return &TunnelsSnapshotBuilder{}
}

// SetTunnelsHandler sets the tunnels handler reference.
func (b *TunnelsSnapshotBuilder) SetTunnelsHandler(h *TunnelsHandler) { b.tunnels = h }

// SetExternalHandler sets the external tunnels handler reference.
func (b *TunnelsSnapshotBuilder) SetExternalHandler(h *ExternalTunnelsHandler) { b.external = h }

// SetSystemTunnelsHandler sets the system tunnels handler reference.
func (b *TunnelsSnapshotBuilder) SetSystemTunnelsHandler(h *SystemTunnelsHandler) { b.systemTun = h }

// Build composes the snapshot payload for the polling store. Returns
// nil when no TunnelsHandler is wired, or when its listItems call
// errors — in both cases there's nothing safe to return.
func (b *TunnelsSnapshotBuilder) Build(ctx context.Context) map[string]interface{} {
	if b.tunnels == nil {
		return nil
	}
	items, err := b.tunnels.listItems(ctx)
	if err != nil {
		return nil
	}
	payload := map[string]interface{}{"tunnels": items}
	if b.external != nil {
		external, _ := b.external.listExternal(ctx)
		payload["external"] = external
	}
	if b.systemTun != nil {
		system, _ := b.systemTun.listSystemTunnels(ctx)
		payload["system"] = system
	}
	return payload
}
