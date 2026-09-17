package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

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
// The stream forwards EVERY bus event unfiltered — push-only ones
// (traffic, connectivity, logs, ping-check logs, sing-box delay/traffic,
// geo download progress, DNS-route failover notifications, the generic
// resource:invalidated hint) and the internal dual-publish ones
// (tunnel:state, tunnel:deleted, pingcheck:state), which the frontend
// simply does not subscribe to. All cold-tier state is fetched via REST
// by the frontend polling stores; the initial "connected" marker lets
// the client confirm the stream is open before any push event arrives.
// sseHeartbeat — как часто слать в поток комментарий-пульс, и sseWriteTimeout —
// сколько ждать одну запись.
//
// Без пульса обрыв ловится ТОЛЬКО через r.Context().Done(), а он приходит лишь
// когда клиент закрылся штатно. Умерший без FIN (потеря Wi-Fi, снятие питания)
// держал бы подписку до TCP-keepalive — десятки минут. Цена этого выросла:
// фоновые опросчики (матрица, sysfs-поллер, delay-проверка) решают по
// ClientCount, нужна ли их работа, и один мёртвый клиент возвращал бы полную
// нагрузку на всё это время.
//
// Переменные, а не константы: иначе пульс нечем проверить — тест не станет
// ждать полминуты.
var (
	sseHeartbeat    = 25 * time.Second
	sseWriteTimeout = 10 * time.Second
)

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

	_, ch, unsubscribe := h.bus.SubscribeClient()
	defer unsubscribe()

	// Send initial "connected" event so client confirms stream works.
	fmt.Fprintf(w, "event: connected\ndata: {\"ok\":true,\"instanceId\":%q}\n\n", h.instanceID)
	flusher.Flush()

	// Дедлайн на запись: без него обращение к мёртвому сокету висит, пока не
	// переполнится буфер ядра, — то есть пульс не сработает как детектор.
	// Слушатель SSE поднят без Read/Write timeout сервера, так что срок
	// ставится здесь, на каждую запись.
	rc := http.NewResponseController(w)

	write := func(format string, args ...any) bool {
		_ = rc.SetWriteDeadline(time.Now().Add(sseWriteTimeout))
		if _, err := fmt.Fprintf(w, format, args...); err != nil {
			return false
		}
		flusher.Flush()
		_ = rc.SetWriteDeadline(time.Time{})
		return true
	}

	beat := time.NewTicker(sseHeartbeat)
	defer beat.Stop()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case <-beat.C:
			// Комментарий SSE: клиент его игнорирует, а мы узнаём об обрыве.
			if !write(": ping\n\n") {
				return
			}
		case event, ok := <-ch:
			if !ok {
				return
			}
			data, err := json.Marshal(event.Data)
			if err != nil {
				continue
			}
			if !write("id: %d\nevent: %s\ndata: %s\n\n", event.ID, event.Type, data) {
				return
			}
		}
	}
}
