package api

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/hoaxisr/awg-manager/internal/events"
)

// EventsHandler serves the SSE event stream.
type EventsHandler struct {
	bus        *events.Bus
	instanceID string
}

// NewEventsHandler creates a new events handler.
func NewEventsHandler(bus *events.Bus, instanceID string) *EventsHandler {
	return &EventsHandler{bus: bus, instanceID: instanceID}
}

// Stream serves the SSE event stream.
// GET /api/events
//
//	@Summary		SSE event stream
//	@Tags			events
//	@Produce		text/event-stream
//	@Security		CookieAuth
//	@Success		200	{string}	string	"Server-Sent Events"
//	@Success		299	{object}	SingboxRouterTransitionData	"Schema of a singbox-router:transition push event carried on this stream (documentation only — never an HTTP status)"
//	@Failure		400	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/events [get]
//
// The stream carries only incremental/push-only events (traffic,
// connectivity, logs, ping-check logs, sing-box delay/traffic, geo
// download progress, DNS-route failover notifications, and the generic
// resource:invalidated hint). All cold-tier state is fetched via REST
// by the frontend polling stores; the initial "connected" marker lets
// the client confirm the stream is open before any push event arrives.
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

	// Send initial "connected" event so client confirms stream works.
	fmt.Fprintf(w, "event: connected\ndata: {\"ok\":true,\"instanceId\":%q}\n\n", h.instanceID)
	flusher.Flush()

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
