package api

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/hoaxisr/awg-manager/internal/events"
)

// EventsHandler serves the SSE event stream.
type EventsHandler struct {
	bus      *events.Bus
	snapshot *SnapshotBuilder
}

// SetSnapshotBuilder sets the snapshot builder for initial state delivery on connect.
func (h *EventsHandler) SetSnapshotBuilder(sb *SnapshotBuilder) {
	h.snapshot = sb
}

// NewEventsHandler creates a new events handler.
func NewEventsHandler(bus *events.Bus) *EventsHandler {
	return &EventsHandler{bus: bus}
}

// Stream serves the SSE event stream.
// GET /api/events
func (h *EventsHandler) Stream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	flusher.Flush()

	_, ch, unsubscribe := h.bus.Subscribe()
	defer unsubscribe()

	// Send initial "connected" event so client confirms stream works
	fmt.Fprintf(w, "event: connected\ndata: {\"ok\":true}\n\n")
	flusher.Flush()

	// Send snapshots on connect (full state for SSE-only architecture).
	if h.snapshot != nil {
		h.snapshot.SendSnapshots(w, flusher, r.Context())
	}

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-ch:
			if !ok {
				return
			}
			data, err := json.Marshal(event.Data)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", event.ID, event.Type, data)
			flusher.Flush()
		}
	}
}
